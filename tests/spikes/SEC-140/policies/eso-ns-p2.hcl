# Variant P2: P1 plus create and update on metadata (custom metadata write).
path "mounted/data/@NS@/*"     { capabilities = ["create", "read"] }
path "mounted/metadata/@NS@/*" { capabilities = ["create", "update", "read", "delete"] }
