# Fallback birth identity (one-shot Job): create a value under its own namespace,
# read metadata to see whether the value already exists. No read of values,
# no update, no delete.
path "mounted/data/@NS@/*"     { capabilities = ["create"] }
path "mounted/metadata/@NS@/*" { capabilities = ["read"] }
