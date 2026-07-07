package helper

func isEphemeralProcess(process string) bool {
	return process == "release" || process == "exec"
}
