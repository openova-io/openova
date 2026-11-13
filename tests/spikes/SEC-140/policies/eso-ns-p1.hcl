# Variant P1: P0 plus read on values.
path "mounted/data/@NS@/*"     { capabilities = ["create", "read"] }
path "mounted/metadata/@NS@/*" { capabilities = ["read", "delete"] }
