# Security
## Introduction
Local communications over the UNIX socket happen over a cleartext HTTP
socket and access is restricted by socket ownership and mode.

Remote communications with the LXD daemon happen using JSON over HTTPS.
The supported protocol must be TLS1.2 or better.
All communications must use perfect forward secrecy and ciphers must be
limited to strong elliptic curve ones (such as ECDHE-RSA or
ECDHE-ECDSA).

Any generated key should be at least 4096bit RSA and when using
signatures, only SHA-2 signatures should be trusted.

Since we control both client and server, there is no reason to support
any backward compatibility to broken protocol or ciphers.

Both the client and the server will generate a keypair the first time
they're launched. The server will use that for all https connections to
the LXD socket and the client will use its certificate as a client
certificate for any client-server communication.

To cause certificates to be regenerated, simply remove the old ones. On the
next connection a new certificate will be generated.

## Adding a remote with a default setup
In the default setup, when the user adds a new server with `lxc remote add`,
the server will be contacted over HTTPs, its certificate downloaded and the
fingerprint will be shown to the user.

The user will then be asked to confirm that this is indeed the server's
fingerprint which they can manually check by connecting to or asking
someone with access to the server to run the status command and compare
the fingerprints.

After that, the user must enter the trust password for that server, if
it matches, the client certificate is added to the server's trust store
and the client can now connect to the server without having to provide
any additional credentials.

This is a workflow that's very similar to that of ssh where an initial
connection to an unknown server triggers a prompt.

A possible extension to that is to support something similar to ssh's
fingerprint in DNS feature where the certificate fingerprint is added as
a TXT record, then if the domain is signed by DNSSEC, the client will
automatically accept the fingerprint if it matches that in the DNS
record.

## Managing trusted clients
The list of certificates trusted by a LXD server can be obtained with `lxc
config trust list`.

To revoke trust to a client its certificate can be removed with `lxc config
trust remove FINGERPRINT`.

## Password prompt
To establish a new trust relationship, a password must be set on the
server and send by the client when adding itself.

A remote add operation should therefore go like this:

 1. Call GET /1.0
 2. Ask the user to confirm the fingerprint.
 3. Look at the dict we received back from the server. If "auth" is
    "untrusted", ask the user for the server's password and do a `POST` to
    `/1.0/certificates`, then call `/1.0` again to check that we're indeed
    trusted.
 4. Remote is now ready

## Failure scenarios
### Server certificate changes
This will typically happen in two cases:

 * The server was fully reinstalled and so changed certificate
 * The connection is being intercepted (MITM)

In such cases the client will refuse to connect to the server since the
certificate fringerprint will not match that in the config for this
remote.

It is then up to the user to contact the server administrator to check
if the certificate did in fact change. If it did, then the certificate
can be replaced by the new one or the remote be removed altogether and
re-added.


### Server trust relationship revoked
In this case, the server still uses the same certificate but all API
calls return a 403 with an error indicating that the client isn't
trusted.

This happens if another trusted client or the local server administrator
removed the trust entry on the server.


## Production setup
For production setup, it's recommended that `core.trust_password` is unset
after all clients have been added.  This prevents brute-force attacks trying to
guess the password.

Furthermore, `core.https_address` should be set to the single address where the
server should be available (rather than any address on the host), and firewall
rules should be set to only allow access to the LXD port from authorized
hosts/subnets.
