use strict;
use warnings;
use IO::Socket::UNIX;
use Socket qw(SOCK_STREAM);

alarm 12;
my $path = $ENV{APP_CERT_ISSUER_SOCKET} or die "CertIssuer socket is unset\n";
my $host = $ENV{APP_CERT_ISSUER_BASE_URL} or die "CertIssuer origin is unset\n";
$host =~ s{^https://}{} or die "CertIssuer origin must use HTTPS\n";
$host =~ s{/.*$}{};
my $socket = IO::Socket::UNIX->new(Type => SOCK_STREAM, Peer => $path)
  or die "CertIssuer socket is unavailable\n";
$socket->autoflush(1);
print {$socket} "POST /v1/certificates/app/issue HTTP/1.1\r\nHost: $host\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}";
local $/;
my $response = <$socket> // '';
$response =~ /\AHTTP\/1\.[01] \d{3}/ or die "CertIssuer socket returned an invalid response\n";
print $response;
