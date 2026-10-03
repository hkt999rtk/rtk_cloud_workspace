use strict;
use warnings;
use JSON::PP qw(decode_json);
use POSIX ();

sub fail {
    my ($code, $message) = @_;
    print STDERR "FAIL [$code]: $message\n";
    exit 1;
}

my $mode = shift @ARGV // '';
if ($mode eq 'bounded') {
    my $seconds = shift @ARGV;
    my $pid = fork();
    defined $pid or fail('PROBE_START_FAILED', 'cannot start probe');
    if ($pid == 0) {
        setpgrp(0, 0) or POSIX::_exit(127);
        exec @ARGV or POSIX::_exit(127);
    }
    my $stop = sub {
        kill 'KILL', -$pid;
        kill 'KILL', $pid;
        waitpid($pid, 0);
    };
    $SIG{ALRM} = sub { $stop->(); exit 124 };
    $SIG{INT} = sub { $stop->(); exit 130 };
    $SIG{TERM} = sub { $stop->(); exit 143 };
    alarm $seconds;
    waitpid($pid, 0);
    my $status = $?;
    alarm 0;
    exit(($status & 127) ? 128 + ($status & 127) : $status >> 8);
}

local $/;
my $raw = <STDIN> // '';
if ($mode eq 'failure') {
    my ($stage, $status) = @ARGV;
    # Only emit allowlisted classifications, never command stderr or credentials.
    fail('PROBE_TIMEOUT', "$stage probe exceeded its time limit")
        if $status == 124 || $status == 142 || $raw =~ /timed out|deadline exceeded|CERTISSUER_SOCKET_TIMEOUT/i;
    fail('K8S_FORBIDDEN', "$stage probe lacks Kubernetes permission") if $raw =~ /\bForbidden\b/i;
    fail('K8S_UNAUTHORIZED', "$stage probe requires Kubernetes authentication") if $raw =~ /\bUnauthorized\b/i;
    fail('TLS_HOSTNAME_INVALID', 'CertIssuer server certificate does not match its hostname')
        if $raw =~ /hostname mismatch|verify error:num=62\b/i;
    fail('TLS_CERTIFICATE_INVALID', 'CertIssuer TLS certificate verification failed; check the CA chain and certificate validity')
        if $raw =~ /certificate verify failed|verify error:num=|self.signed certificate|unable to get.*issuer/i;
    fail('TLS_HANDSHAKE_FAILED', 'CertIssuer TLS handshake failed; check client identity and TLS settings')
        if $raw =~ /ssl.*alert|tls.*alert|handshake failure/i;
    fail('CONFIG_INVALID', 'CertIssuer HTTPS origin or client certificate settings are incomplete')
        if $raw =~ /CERTISSUER_CONFIG_INVALID|CertIssuer (?:origin|socket) is unset|origin must use HTTPS/;
    fail('TOOL_MISSING', "$stage probe requires a missing executable or runtime module")
        if $status == 127 || $raw =~ /command not found|: not found|Can.t locate .* in \@INC/;
    fail('SOCKET_UNAVAILABLE', 'managed CertIssuer socket is unavailable')
        if $raw =~ /CertIssuer socket is unavailable/;
    fail('RESPONSE_INVALID', 'managed CertIssuer socket returned an invalid HTTP response')
        if $raw =~ /CertIssuer socket returned an invalid response/;
    fail('TRANSPORT_FAILED', "$stage probe could not complete; check Kubernetes connectivity and the selected workload");
}
if ($mode eq 'pod') {
    my $list = eval { decode_json($raw) };
    fail('POD_RESPONSE_INVALID', 'Account Manager Pod list is not valid JSON')
        if $@ || ref($list) ne 'HASH' || ref($list->{items}) ne 'ARRAY';
    my @ready = sort map { $_->{metadata}{name} } grep {
        ref($_) eq 'HASH' && ref($_->{metadata}) eq 'HASH' && ref($_->{status}) eq 'HASH'
        && !$_->{metadata}{deletionTimestamp} && ($_->{status}{phase} // '') eq 'Running'
        && ($_->{metadata}{name} // '') =~ /\A[a-z0-9][a-z0-9.-]*\z/
        && ref($_->{status}{conditions}) eq 'ARRAY'
        && grep { ref($_) eq 'HASH' && ($_->{type} // '') eq 'Ready' && ($_->{status} // '') eq 'True' } @{$_->{status}{conditions}}
    } @{$list->{items}};
    fail('POD_NOT_READY', 'no Ready, nonterminating Account Manager Pod is available') unless @ready;
    print $ready[0];
    exit 0;
}
if ($mode eq 'response') {
    my $transport = shift @ARGV // '';
    fail('RESPONSE_INVALID', 'CertIssuer returned an invalid HTTP response')
        unless $raw =~ /\AHTTP\/1\.[01] ([0-9]{3})[^\r\n]*\r?\n/;
    my $status = 0 + $1;
    fail('AUTH_REJECTED', "CertIssuer rejected authorization (HTTP $status)") if $status == 401 || $status == 403;
    fail('UPSTREAM_UNAVAILABLE', "CertIssuer service is unavailable (HTTP $status)") if $status >= 500;
    my ($headers, $body) = split /\r?\n\r?\n/, $raw, 2;
    fail('RESPONSE_INVALID', 'CertIssuer HTTP response has no complete header/body boundary') unless defined $body;
    # Go HTTP servers may stream a small JSON error using chunked encoding.
    if ($headers =~ /^Transfer-Encoding:\s*chunked\s*\r?$/im) {
        my $decoded = '';
        while (1) {
            fail('RESPONSE_INVALID', 'CertIssuer returned invalid HTTP chunks')
                unless $body =~ s/\A([0-9a-fA-F]+)(?:;[^\r\n]*)?\r?\n//;
            my $size = hex $1;
            last if $size == 0;
            fail('RESPONSE_INVALID', 'CertIssuer returned a truncated HTTP chunk') if length($body) < $size;
            $decoded .= substr($body, 0, $size, '');
            fail('RESPONSE_INVALID', 'CertIssuer returned invalid HTTP chunks') unless $body =~ s/\A\r?\n//;
        }
        $body = $decoded;
    }
    my $payload = eval { decode_json($body) };
    fail('RESPONSE_JSON_INVALID', 'CertIssuer response body is not a JSON object') if $@ || ref($payload) ne 'HASH';
    my $code = ref($payload->{error}) eq 'HASH' ? $payload->{error}{code} : undef;
    fail('VALIDATION_CONTRACT_MISMATCH', "expected HTTP 400 with user_id_required; received HTTP $status with a different validation result")
        unless $status == 400 && defined($code) && !ref($code) && $code eq 'user_id_required';
    print $transport eq 'socket'
        ? "PASS: managed CertIssuer socket request reached authenticated request validation\n"
        : "PASS: CertIssuer verified TLS endpoint authorized the Account Manager mTLS identity and reached request validation\n";
    exit 0;
}
fail('CONFIG_INVALID', 'unknown CertIssuer helper operation');
