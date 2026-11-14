# Seal hook test (contract C8, docs/contracts/c8-seal-provider.md).
#
# Every run applies the hook module ./seal by itself, not the provider
# module. The hook needs only the random provider, which calls no API, so the
# runs make real 32-byte keys in memory and reach no cloud.
#
# What the runs prove: the outputs of the contract; one key per region; the
# key identifier is the first 16 hexadecimal characters of the SHA-256 of the
# key's base64 text; the stanza names the key file and never holds key bytes;
# the node file is the key file under /var/lib/catalyst/seal, mode 0400,
# owner 100:1000; the volume device of this pack is /dev/vdb.

run "hook_one_region" {
  command = apply

  module {
    source = "./seal"
  }

  variables {
    region_keys = ["region-a"]
  }

  assert {
    condition     = output.kind == "static-node-file"
    error_message = "SEC-150: the hook's output kind must be static-node-file."
  }

  assert {
    condition     = output.volume_device == "/dev/vdb"
    error_message = "SEC-150: the hook's output volume_device of this pack must be \"/dev/vdb\"."
  }

  assert {
    condition     = join(",", keys(output.stanza_by_region)) == "region-a" && join(",", keys(output.node_files_by_region)) == "region-a"
    error_message = "SEC-150: the hook must return one stanza and one list of node files per region key."
  }

  assert {
    condition     = length(output.node_files_by_region["region-a"]) == 1
    error_message = "SEC-150: the static hook must return exactly one node file per region."
  }

  assert {
    condition     = can(regex("^/var/lib/catalyst/seal/openbao-seal-[0-9a-f]{16}\\.key$", output.node_files_by_region["region-a"][0].path))
    error_message = "SEC-150: the node file path must be /var/lib/catalyst/seal/openbao-seal-<16 hexadecimal characters>.key."
  }

  assert {
    condition     = output.node_files_by_region["region-a"][0].owner == "100:1000" && output.node_files_by_region["region-a"][0].permissions == "0400"
    error_message = "SEC-150: the node file must have owner 100:1000 and permissions 0400."
  }

  assert {
    condition     = can(regex("^[A-Za-z0-9+/]{43}=$", output.node_files_by_region["region-a"][0].content_b64))
    error_message = "SEC-150: content_b64 must be the base64 text of exactly 32 bytes (43 characters and one =)."
  }

  assert {
    condition     = output.node_files_by_region["region-a"][0].path == "/var/lib/catalyst/seal/openbao-seal-${substr(sha256(output.node_files_by_region["region-a"][0].content_b64), 0, 16)}.key"
    error_message = "SEC-150: the key identifier in the file name must be the first 16 hexadecimal characters of the SHA-256 of the key's base64 text."
  }

  assert {
    condition     = output.stanza_by_region["region-a"] == "seal \"static\" {\n  current_key_id = \"${substr(sha256(output.node_files_by_region["region-a"][0].content_b64), 0, 16)}\"\n  current_key    = \"file:///openbao/seal/openbao-seal-${substr(sha256(output.node_files_by_region["region-a"][0].content_b64), 0, 16)}.key\"\n}\n"
    error_message = "SEC-150: the stanza must be the seal \"static\" block of contract C8 with the key identifier and the file reference of this region's key."
  }

  assert {
    condition     = !strcontains(output.stanza_by_region["region-a"], output.node_files_by_region["region-a"][0].content_b64)
    error_message = "SEC-150: the stanza must never hold key bytes."
  }
}

run "hook_two_regions" {
  command = apply

  module {
    source = "./seal"
  }

  variables {
    region_keys = ["region-a", "region-b"]
  }

  assert {
    condition     = join(",", keys(output.stanza_by_region)) == "region-a,region-b" && join(",", keys(output.node_files_by_region)) == "region-a,region-b"
    error_message = "SEC-150: two region keys must give two stanzas and two lists of node files."
  }

  assert {
    condition     = output.node_files_by_region["region-a"][0].content_b64 != output.node_files_by_region["region-b"][0].content_b64
    error_message = "SEC-150: every region must get its own seal key."
  }

  assert {
    condition     = output.node_files_by_region["region-a"][0].path != output.node_files_by_region["region-b"][0].path
    error_message = "SEC-150: the key files of two regions must have different key identifiers."
  }

  assert {
    condition     = strcontains(output.stanza_by_region["region-b"], substr(sha256(output.node_files_by_region["region-b"][0].content_b64), 0, 16)) && !strcontains(output.stanza_by_region["region-b"], substr(sha256(output.node_files_by_region["region-a"][0].content_b64), 0, 16))
    error_message = "SEC-150: the stanza of a region must name that region's key and no other."
  }
}
