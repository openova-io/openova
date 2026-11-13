# Throwaway SSH target of lab test ACC-020: password login for user "spike"
# and certificate login for any key signed by the user CA mounted at
# /etc/ssh/ca/trusted-user-ca.pub (the development OpenBao SSH mount's key).
# No authorized_keys: a bare key without a certificate is refused.
FROM docker.io/library/alpine:3.20.3
RUN apk add --no-cache openssh-server openssh-keygen && ssh-keygen -A \
 && adduser -D -s /bin/sh spike && echo 'spike:spike-pass-020' | chpasswd
RUN printf '%s\n' \
  'Port 22' \
  'PermitRootLogin no' \
  'PasswordAuthentication yes' \
  'KbdInteractiveAuthentication no' \
  'PubkeyAuthentication yes' \
  'AuthorizedKeysFile none' \
  'TrustedUserCAKeys /etc/ssh/ca/trusted-user-ca.pub' \
  'LogLevel VERBOSE' \
  'Subsystem sftp internal-sftp' \
  > /etc/ssh/sshd_config
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
