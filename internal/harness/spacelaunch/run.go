package spacelaunch

// Run dispatches the Spaces plugin support commands. args is the argument
// vector without the program name; args[0] is the subcommand.
func Run(args []string) int {
	if len(args) == 0 {
		return dief("usage: zj __space-harnesses | __space-launch-agent SESSION AGENT [--profile NAME] [ARGS...]")
	}
	switch args[0] {
	case "__space-harnesses":
		return SpaceHarnesses(args[1:])
	case "__space-launch-agent":
		return SpaceLaunchAgent(args[1:])
	default:
		return dief("unknown command '%s'", args[0])
	}
}
