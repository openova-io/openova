package provisioner

// WriteTfvars writes tofu.auto.tfvars.json into dir for req, with the code
// that Provision runs after Validate. The command tfvars-regenerate rebuilds a
// deployment's variables file with it from the deployment record, and gate
// check CLD-G12 plans that file against a copy of the deployment's working
// directory (work package CLD-094).
func WriteTfvars(dir string, req Request) error {
	return writeTfvars(dir, req)
}
