use strict;
use warnings;
use Fcntl qw(O_RDONLY O_NOFOLLOW O_NONBLOCK F_SETFD);
use POSIX ();

# Runs only in the existing pkimanagement owner. Private PEM bytes are passed to
# OpenSSL through anonymous pipes, never argv, environment, files or output.
my @children;
my @inputs;
sub stop_children {
    kill 'KILL', -$_, $_ for @children;
    waitpid($_, 0) for @children;
    @children = ();
}
sub fail { stop_children(); die "$_[0]\n" }
$SIG{ALRM} = sub { fail('CERTISSUER_MANAGED_PROBE_TIMEOUT') };
$SIG{INT} = $SIG{TERM} = sub { fail('CERTISSUER_MANAGED_PROBE_CANCELLED') };
alarm 12;

sub child_signals {
    alarm 0;
    $SIG{ALRM} = $SIG{INT} = $SIG{TERM} = 'DEFAULT';
    setpgrp(0, 0) or POSIX::_exit(127);
}
sub capture {
    my ($input, $close_inputs, @command) = @_;
    pipe(my $out, my $out_writer) && pipe(my $err, my $err_writer)
        or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
    my $pid = fork();
    defined $pid or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
    if ($pid == 0) {
        child_signals();
        close $out; close $err;
        if ($input) { open STDIN, '<&', $input or POSIX::_exit(127) }
        else { open STDIN, '<', '/dev/null' or POSIX::_exit(127) }
        open STDOUT, '>&', $out_writer or POSIX::_exit(127);
        open STDERR, '>&', $err_writer or POSIX::_exit(127);
        close $out_writer; close $err_writer;
        exec @command or POSIX::_exit(127);
    }
    push @children, $pid;
    close $out_writer; close $err_writer;
    close $_ for @$close_inputs;
    my ($stdout, $stderr) = ('', '');
    my %streams = (fileno($out) => [$out, \$stdout], fileno($err) => [$err, \$stderr]);
    while (%streams) {
        my $ready = '';
        vec($ready, $_, 1) = 1 for keys %streams;
        select($ready, undef, undef, undef) >= 0 or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
        for my $fd (keys %streams) {
            next unless vec($ready, $fd, 1);
            my ($handle, $buffer) = @{$streams{$fd}};
            my $count = sysread($handle, my $part, 4096);
            defined $count or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
            if (!$count) { close $handle; delete $streams{$fd}; next }
            $$buffer .= $part;
            length($stdout) + length($stderr) <= 65536 or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
        }
    }
    waitpid($pid, 0);
    my $status = $?;
    @children = grep { $_ != $pid } @children;
    return ($stdout, $stderr, $status);
}
sub pipe_input {
    my ($bytes) = @_;
    pipe(my $reader, my $writer) or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
    my $pid = fork();
    defined $pid or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
    if ($pid == 0) {
        child_signals();
        close $reader;
        close $_ for @inputs;
        my $ok = print {$writer} $bytes;
        $ok = close($writer) && $ok;
        POSIX::_exit($ok ? 0 : 1);
    }
    push @children, $pid;
    close $writer;
    fcntl($reader, F_SETFD, 0) or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
    push @inputs, $reader;
    return $reader;
}
sub reap_inputs {
    for my $pid (@children) {
        waitpid($pid, 0);
        $? == 0 or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
    }
    @children = (); @inputs = ();
}

my ($host, $socket, $environment, $mode) = @ARGV;
$mode //= 'authenticated';
my $state_path = $ENV{PKI_MANAGEMENT_IDENTITY_STATE} // '';
my $root = $ENV{PKI_MANAGEMENT_IDENTITY_ROOT_SHA256} // '';
my $ca = $ENV{PKI_MANAGEMENT_ISSUER_TLS_CA} // '';
sub canonical_path { $_[0] =~ m{\A/[A-Za-z0-9_./-]+\z} && $_[0] !~ m{//|/(?:\.|\.\.)(?:/|\z)} }
(@ARGV == 3 || @ARGV == 4) && ($mode eq 'authenticated' || $mode eq 'anonymous')
    && $host =~ /\A[a-zA-Z0-9][a-zA-Z0-9.-]*[a-zA-Z0-9]\z/
    && canonical_path($socket) && ($ENV{PKI_MANAGEMENT_SOCKET} // '') eq $socket
    && $environment =~ /\A[a-zA-Z0-9][a-zA-Z0-9_.-]*\z/ && ($ENV{PKI_ENVIRONMENT} // '') eq $environment
    && canonical_path($state_path) && $root =~ /\A[0-9a-f]{64}\z/
    && canonical_path($ca) && -f $ca && -r $ca
    or fail('CERTISSUER_CONFIG_INVALID');

my $request = "POST /v1/certificates/app/issue HTTP/1.1\r\nHost: $host\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}";
if ($mode eq 'anonymous') {
    my $input = pipe_input($request);
    my ($stdout, $stderr, $status) = capture($input, \@inputs,
        'openssl', 's_client', '-quiet', '-connect', "$host:443", '-servername', $host,
        '-verify_return_error', '-verify_hostname', $host, '-CAfile', $ca);
    reap_inputs();
    my $result = $stdout.$stderr;
    $status != 0 && $result =~ /(?:tlsv1(?:\.3)?|ssl).*alert certificate required/i
        && $result !~ /HTTP\/1\.[01] [0-9]{3}|verify error:num=|certificate verify failed|hostname mismatch/i
        or fail('CERTISSUER_ANONYMOUS_DENIAL_UNVERIFIED');
    alarm 0;
    print "tlsv13 alert certificate required\nRTK_ANONYMOUS_EXIT=1\n";
    exit 0;
}

sub inspect_state {
    my ($summary, undef, $status) = capture(undef, [], '/app/serviceidentity-bootstrap', 'inspect', $state_path, 'service:account-manager', $root);
    $status == 0 && $summary =~ /\A\{"subject":"service:account-manager","fingerprint":"[0-9a-f]{64}","public_key_sha256":"[0-9a-f]{64}","root_sha256":"\Q$root\E","state_sha256":"([0-9a-f]{64})","pending":false\}\n?\z/
        or fail('CERTISSUER_MANAGED_IDENTITY_INVALID');
    return $1;
}
my $before = inspect_state();
sysopen(my $state_file, $state_path, O_RDONLY | O_NOFOLLOW | O_NONBLOCK)
    or fail('CERTISSUER_MANAGED_IDENTITY_INVALID');
my @info = stat($state_file);
@info && -f $state_file && ($info[2] & 0777) == 0600 && $info[4] == $> && $info[7] <= 262144
    or fail('CERTISSUER_MANAGED_IDENTITY_INVALID');
my $raw = '';
while (1) {
    my $count = sysread($state_file, my $part, 4096);
    defined $count or fail('CERTISSUER_MANAGED_IDENTITY_INVALID');
    last unless $count;
    $raw .= $part;
    length($raw) <= 262144 or fail('CERTISSUER_MANAGED_IDENTITY_INVALID');
}
close $state_file;
my $hash_input = pipe_input($raw);
my ($hash, undef, $hash_status) = capture($hash_input, \@inputs, 'sha256sum');
reap_inputs();
$hash_status == 0 && $hash =~ /\A\Q$before\E\s+-\n?\z/ or fail('CERTISSUER_MANAGED_STATE_CHANGED');

# Accept only the canonical Go-generated client schema already validated by
# native inspect. Unknown/pending/server states never select another identity.
my $string = qr/"(?:[^"\\\x00-\x1f\x7f-\xff]|\\["\\\/bfnrt]|\\u00[0-7][0-9a-fA-F])*"/;
$raw =~ /\A\s*\{\s*"version"\s*:\s*1\s*,\s*"subject"\s*:\s*"service:account-manager"\s*,\s*"current"\s*:\s*\{\s*"private_key_pem"\s*:\s*($string)\s*,\s*"certificate_chain_pem"\s*:\s*($string)\s*,\s*"installed_at"\s*:\s*$string\s*\}\s*\}\s*\z/
    or fail('CERTISSUER_MANAGED_IDENTITY_INVALID');
my ($key_json, $cert_json) = ($1, $2);
sub decode_string {
    my $value = substr($_[0], 1, -1);
    my %escape = ('"' => '"', '\\' => '\\', '/' => '/', b => "\b", f => "\f", n => "\n", r => "\r", t => "\t");
    $value =~ s/\\(?:u00([0-7][0-9a-fA-F])|(["\\\/bfnrt]))/defined($1) ? chr(hex($1)) : $escape{$2}/ge;
    return $value;
}
my ($key, $cert) = (decode_string($key_json), decode_string($cert_json));
my $cert_input = pipe_input($cert);
my $chain_input = pipe_input($cert);
my $key_input = pipe_input($key);
my $request_input = pipe_input($request);
my ($response, $diagnostic, $status) = capture($request_input, \@inputs,
    'openssl', 's_client', '-quiet', '-connect', "$host:443", '-servername', $host,
    '-verify_return_error', '-verify_hostname', $host,
    '-cert', '/dev/fd/'.fileno($cert_input), '-cert_chain', '/dev/fd/'.fileno($chain_input),
    '-key', '/dev/fd/'.fileno($key_input), '-CAfile', $ca);
reap_inputs();
if ($status != 0) {
    fail('CERTISSUER_TLS_HOSTNAME_INVALID') if $diagnostic =~ /hostname mismatch|verify error:num=62\b/i;
    fail('CERTISSUER_TLS_CERTIFICATE_INVALID') if $diagnostic =~ /certificate verify failed|verify error:num=|self.signed certificate|unable to get.*issuer/i;
    fail('CERTISSUER_TLS_HANDSHAKE_FAILED') if $diagnostic =~ /ssl.*alert|tls.*alert|handshake failure/i;
    fail('CERTISSUER_MANAGED_TRANSPORT_FAILED');
}
inspect_state() eq $before or fail('CERTISSUER_MANAGED_STATE_CHANGED');
$response =~ /\AHTTP\/1\.[01] \d{3}/ or fail('CERTISSUER_MANAGED_RESPONSE_INVALID');
alarm 0;
print $response;
