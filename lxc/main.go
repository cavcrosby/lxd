package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lxc/lxd/lxc/config"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/gnuflag"
	"github.com/lxc/lxd/shared/i18n"
	"github.com/lxc/lxd/shared/logger"
	"github.com/lxc/lxd/shared/logging"
	"github.com/lxc/lxd/shared/version"
)

var configPath string
var execName string

func main() {
	execName = os.Args[0]

	if err := run(); err != nil {
		msg := fmt.Sprintf(i18n.G("error: %v"), err)

		lxdErr := getLocalErr(err)
		switch lxdErr {
		case syscall.ENOENT:
			msg = i18n.G("LXD socket not found; is LXD installed and running?")
		case syscall.ECONNREFUSED:
			msg = i18n.G("Connection refused; is LXD running?")
		case syscall.EACCES:
			msg = i18n.G("Permission denied, are you in the lxd group?")
		}

		fmt.Fprintln(os.Stderr, fmt.Sprintf("%s", msg))
		os.Exit(1)
	}
}

func run() error {
	verbose := gnuflag.Bool("verbose", false, i18n.G("Enable verbose mode"))
	debug := gnuflag.Bool("debug", false, i18n.G("Enable debug mode"))
	forceLocal := gnuflag.Bool("force-local", false, i18n.G("Force using the local unix socket"))
	noAlias := gnuflag.Bool("no-alias", false, i18n.G("Ignore aliases when determining what command to run"))

	var configDir string
	if os.Getenv("LXD_CONF") != "" {
		configDir = os.Getenv("LXD_CONF")
	} else if os.Getenv("HOME") != "" {
		configDir = path.Join(os.Getenv("HOME"), ".config", "lxc")
	} else {
		user, err := user.Current()
		if err != nil {
			return err
		}

		configDir = path.Join(user.HomeDir, ".config", "lxc")
	}
	configPath = os.ExpandEnv(path.Join(configDir, "config.yml"))

	if len(os.Args) >= 3 && os.Args[1] == "config" && os.Args[2] == "profile" {
		fmt.Fprintf(os.Stderr, i18n.G("`lxc config profile` is deprecated, please use `lxc profile`")+"\n")
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	if len(os.Args) >= 2 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		os.Args[1] = "help"
	}

	if len(os.Args) >= 2 && (os.Args[1] == "--all") {
		os.Args = []string{os.Args[0], "help", "--all"}
	}

	if shared.StringInSlice("--version", os.Args) && !shared.StringInSlice("--", os.Args) {
		os.Args = []string{os.Args[0], "version"}
	}

	if len(os.Args) < 2 {
		commands["help"].run(nil, nil)
		os.Exit(1)
	}

	var conf *config.Config
	var err error

	if *forceLocal {
		conf = &config.DefaultConfig
	} else if shared.PathExists(configPath) {
		conf, err = config.LoadConfig(configPath)
		if err != nil {
			return err
		}
	} else {
		conf = &config.DefaultConfig
		conf.ConfigDir = filepath.Dir(configPath)
	}

	// Set the user agent
	conf.UserAgent = version.UserAgent

	// This is quite impolite, but it seems gnuflag needs us to shift our
	// own exename out of the arguments before parsing them. However, this
	// is useful for execIfAlias, which wants to know exactly the command
	// line we received, and in some cases is called before this shift, and
	// in others after. So, let's save the original args.
	origArgs := os.Args
	name := os.Args[1]

	/* at this point we haven't parsed the args, so we have to look for
	 * --no-alias by hand.
	 */
	if !shared.StringInSlice("--no-alias", origArgs) {
		execIfAliases(conf, origArgs)
	}
	cmd, ok := commands[name]
	if !ok {
		commands["help"].run(nil, nil)
		fmt.Fprintf(os.Stderr, "\n"+i18n.G("error: unknown command: %s")+"\n", name)
		os.Exit(1)
	}
	cmd.flags()
	gnuflag.Usage = func() {
		fmt.Print(cmd.usage())
		fmt.Printf("\n\n%s\n", i18n.G("Options:"))

		gnuflag.SetOut(os.Stdout)
		gnuflag.PrintDefaults()
		os.Exit(0)
	}

	os.Args = os.Args[1:]
	gnuflag.Parse(true)

	logger.Log, err = logging.GetLogger("", "", *verbose, *debug, nil)
	if err != nil {
		return err
	}

	certf := conf.ConfigPath("client.crt")
	keyf := conf.ConfigPath("client.key")

	if !*forceLocal && os.Args[0] != "help" && os.Args[0] != "version" && (!shared.PathExists(certf) || !shared.PathExists(keyf)) {
		fmt.Fprintf(os.Stderr, i18n.G("Generating a client certificate. This may take a minute...")+"\n")

		err = shared.FindOrGenCert(certf, keyf, true)
		if err != nil {
			return err
		}

		if shared.PathExists("/var/lib/lxd/") {
			fmt.Fprintf(os.Stderr, i18n.G("If this is your first time running LXD on this machine, you should also run: lxd init")+"\n")
			fmt.Fprintf(os.Stderr, i18n.G("To start your first container, try: lxc launch ubuntu:16.04")+"\n\n")
		}
	}

	err = cmd.run(conf, gnuflag.Args())
	if err == errArgs || err == errUsage {
		out := os.Stdout
		if err == errArgs {
			/* If we got an error about invalid arguments, let's try to
			 * expand this as an alias
			 */
			if !*noAlias {
				execIfAliases(conf, origArgs)
			}

			out = os.Stderr
		}
		gnuflag.SetOut(out)

		if err == errArgs {
			fmt.Fprintf(out, i18n.G("error: %v"), err)
			fmt.Fprintf(out, "\n\n")
		}
		fmt.Fprint(out, cmd.usage())
		fmt.Fprintf(out, "\n\n%s\n", i18n.G("Options:"))

		gnuflag.PrintDefaults()

		if err == errArgs {
			os.Exit(1)
		}
		os.Exit(0)
	}

	return err
}

type command interface {
	usage() string
	flags()
	showByDefault() bool
	run(config *config.Config, args []string) error
}

var commands = map[string]command{
	"config":  &configCmd{},
	"copy":    &copyCmd{},
	"delete":  &deleteCmd{},
	"exec":    &execCmd{},
	"file":    &fileCmd{},
	"finger":  &fingerCmd{},
	"help":    &helpCmd{},
	"image":   &imageCmd{},
	"info":    &infoCmd{},
	"init":    &initCmd{},
	"launch":  &launchCmd{},
	"list":    &listCmd{},
	"manpage": &manpageCmd{},
	"monitor": &monitorCmd{},
	"move":    &moveCmd{},
	"pause": &actionCmd{
		action:      shared.Freeze,
		description: i18n.G("Pause containers."),
		name:        "pause",
	},
	"profile": &profileCmd{},
	"publish": &publishCmd{},
	"remote":  &remoteCmd{},
	"restart": &actionCmd{
		action:      shared.Restart,
		description: i18n.G("Restart containers."),
		hasTimeout:  true,
		visible:     true,
		name:        "restart",
		timeout:     -1,
	},
	"restore":  &restoreCmd{},
	"snapshot": &snapshotCmd{},
	"start": &actionCmd{
		action:      shared.Start,
		description: i18n.G("Start containers."),
		visible:     true,
		name:        "start",
	},
	"stop": &actionCmd{
		action:      shared.Stop,
		description: i18n.G("Stop containers."),
		hasTimeout:  true,
		visible:     true,
		name:        "stop",
		timeout:     -1,
	},
	"version": &versionCmd{},
}

var errArgs = fmt.Errorf(i18n.G("wrong number of subcommand arguments"))
var errUsage = fmt.Errorf("show usage")

func expandAlias(config *config.Config, origArgs []string) ([]string, bool) {
	foundAlias := false
	aliasKey := []string{}
	aliasValue := []string{}

	for k, v := range config.Aliases {
		foundAlias = true
		for i, key := range strings.Split(k, " ") {
			if len(origArgs) <= i+1 || origArgs[i+1] != key {
				foundAlias = false
				break
			}
		}

		if foundAlias {
			aliasKey = strings.Split(k, " ")
			aliasValue = strings.Split(v, " ")
			break
		}
	}

	if !foundAlias {
		return []string{}, false
	}

	newArgs := []string{origArgs[0]}
	hasReplacedArgsVar := false

	for i, aliasArg := range aliasValue {
		if aliasArg == "@ARGS@" && len(origArgs) > i {
			newArgs = append(newArgs, origArgs[i+1:]...)
			hasReplacedArgsVar = true
		} else {
			newArgs = append(newArgs, aliasArg)
		}
	}

	if !hasReplacedArgsVar {
		/* add the rest of the arguments */
		newArgs = append(newArgs, origArgs[len(aliasKey)+1:]...)
	}

	/* don't re-do aliases the next time; this allows us to have recursive
	 * aliases, e.g. `lxc list` to `lxc list -c n`
	 */
	newArgs = append(newArgs[:2], append([]string{"--no-alias"}, newArgs[2:]...)...)

	return newArgs, true
}

func execIfAliases(config *config.Config, origArgs []string) {
	newArgs, expanded := expandAlias(config, origArgs)
	if !expanded {
		return
	}

	path, err := exec.LookPath(origArgs[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.G("processing aliases failed %s\n"), err)
		os.Exit(5)
	}
	ret := syscall.Exec(path, newArgs, syscall.Environ())
	fmt.Fprintf(os.Stderr, i18n.G("processing aliases failed %s\n"), ret)
	os.Exit(5)
}
