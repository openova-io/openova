# Any pod: read its own service-account subtree and its namespace's shared subtree.
# @NS@ and @SA@ are replaced by identity templates on the kubernetes auth accessor.
path "mounted/data/@NS@/sa/@SA@/*" { capabilities = ["read"] }
path "mounted/data/@NS@/shared/*"  { capabilities = ["read"] }
