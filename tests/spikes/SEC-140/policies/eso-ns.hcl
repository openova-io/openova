# Variant P0 (as designed): the namespace's External Secrets identity may create
# a value under its own namespace, and read or delete metadata, but never read a value.
path "mounted/data/@NS@/*"     { capabilities = ["create"] }
path "mounted/metadata/@NS@/*" { capabilities = ["read", "delete"] }
