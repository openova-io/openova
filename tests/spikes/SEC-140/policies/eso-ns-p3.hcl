# Variant P3: P2 plus update on values (External Secrets writes metadata first,
# so its value write is an update of an existing key).
path "mounted/data/@NS@/*"     { capabilities = ["create", "read", "update"] }
path "mounted/metadata/@NS@/*" { capabilities = ["create", "update", "read", "delete"] }
