package main

import "time"

// defaultPresentTimeout bounds an `AddRecord` call. Dynadot's api3.json
// is occasionally slow on cold starts; cert-manager retries Present
// every 30s on failure so a 25s timeout keeps each individual attempt
// short of the retry interval.
const defaultPresentTimeout = 25 * time.Second

// defaultCleanUpTimeout bounds a `RemoveSubRecord` call. RemoveSubRecord
// performs a read-modify-write (`domain_info` then `set_dns2`) so the
// budget is doubled vs. Present.
const defaultCleanUpTimeout = 50 * time.Second
