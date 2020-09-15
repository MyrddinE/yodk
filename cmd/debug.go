package cmd

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dbaumgarten/yodk/pkg/debug"

	"github.com/abiosoft/ishell"
	"github.com/dbaumgarten/yodk/pkg/vm"
	"github.com/spf13/cobra"
)

// cli args passed to this command
var debugShell *ishell.Shell

// number of the case in the given test to execute
var caseNumber int

// the debug-helper used by the cli-debugger
var helper *debug.Helper

// the args the script was called with
var cliargs []string

var running bool

var ignoreErrs bool

// debugCmd represents the debug command
var debugCmd = &cobra.Command{
	Use:   "debug [script]+ / debug [testfile]",
	Short: "Debug yolol/nolol programs or tests",
	Long:  `Execute programs interactively in debugger`,
	Run: func(cmd *cobra.Command, args []string) {
		cliargs = args
		load(args)
		debugShell.Run()
	},
	Args: cobra.MinimumNArgs(1),
}

// load input scripts
// decide whether to load a bunch of scripts or a test-file
func load(args []string) {
	containsScript := false
	containsTest := false
	running = false
	for _, arg := range args {
		if strings.HasSuffix(arg, ".yaml") {
			containsTest = true
		} else if strings.HasSuffix(arg, ".yolol") || strings.HasSuffix(arg, ".nolol") {
			containsScript = true
		} else {
			fmt.Println("Unknown file-extension for file: ", arg)
			os.Exit(1)
		}
	}

	if containsScript && containsTest {
		fmt.Println("Can not mix test-files and scripts.")
		os.Exit(1)
	}

	if len(args) > 1 && containsTest {
		fmt.Println("Can only debug one test at once")
		os.Exit(1)
	}

	var err error
	if containsTest {
		helper, err = debug.FromTest("", args[0], caseNumber, prepareVM)
	} else {
		helper, err = debug.FromScripts("", args, prepareVM)
	}
	helper.IgnoreErrs = ignoreErrs
	exitOnError(err, "starting debugger")

	debugShell.Println("Loaded and paused programs. Enter 'c' to start execution.")
}

// prepares the given VM for use in the debugger
func prepareVM(thisVM *vm.VM, inputFileName string) {
	thisVM.SetBreakpointHandler(func(x *vm.VM) bool {
		debugShell.Printf("--Hit Breakpoint at %s:%d--\n", inputFileName, x.CurrentSourceLine())
		return false
	})
	thisVM.SetErrorHandler(func(x *vm.VM, err error) bool {
		if !helper.IgnoreErrs {
			debugShell.Printf("--A runtime error occured at %s:%d--\n", inputFileName, x.CurrentSourceLine())
			debugShell.Println(err)
			debugShell.Println("--Execution paused--")
			return false
		}
		return true
	})
	thisVM.SetFinishHandler(func(x *vm.VM) {
		debugShell.Printf("--Program %s finished--\n", inputFileName)
	})
	thisVM.SetStepHandler(func(x *vm.VM) {
		debugShell.Printf("--Step executed. VM paused at %s:%d--\n", inputFileName, x.CurrentSourceLine())
	})
}

// initialize the shell
func init() {
	debugCmd.Flags().IntVarP(&caseNumber, "case", "c", 1, "Numer of the case to execute when debugging a test")
	debugCmd.Flags().BoolVarP(&ignoreErrs, "ignore", "i", false, "If true, ignore runtime-errors when debugging scripts")

	rootCmd.AddCommand(debugCmd)

	debugShell = ishell.New()

	debugShell.AddCmd(&ishell.Cmd{
		Name:    "reset",
		Aliases: []string{"r"},
		Help:    "reset debugger",
		Func: func(c *ishell.Context) {
			helper.Coordinator.Terminate()
			load(cliargs)
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "scripts",
		Aliases: []string{"ll"},
		Help:    "list scripts",
		Func: func(c *ishell.Context) {
			for i, file := range helper.ScriptNames {
				line := "  "
				if i == helper.CurrentScript {
					line = "> "
				}
				line += file
				debugShell.Println(line)
			}
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "choose",
		Aliases: []string{"cd"},
		Help:    "change currently viewed script",
		Func: func(c *ishell.Context) {
			if len(c.Args) != 1 {
				debugShell.Println("You must enter a script name (run scripts to list them).")
				return
			}
			for i, file := range helper.ScriptNames {
				if file == c.Args[0] {
					helper.CurrentScript = i
					debugShell.Printf("--Changed to %s--\n", file)
					return
				}
			}
			debugShell.Printf("--Unknown script %s--", c.Args[0])
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "pause",
		Aliases: []string{"p"},
		Help:    "pause execution",
		Func: func(c *ishell.Context) {
			helper.Vms[helper.CurrentScript].Pause()
			debugShell.Println("--Paused--")
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "continue",
		Aliases: []string{"c"},
		Help:    "continue paused execution",
		Func: func(c *ishell.Context) {
			if !running {
				running = true
				helper.Coordinator.Run()
				return
			}
			if helper.Vms[helper.CurrentScript].State() != vm.StatePaused {
				debugShell.Println("The current script is not paused.")
				return
			}
			helper.Vms[helper.CurrentScript].Resume()
			debugShell.Println("--Resumed--")
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "step",
		Aliases: []string{"s"},
		Help:    "execute the next line and pause again",
		Func: func(c *ishell.Context) {
			if !running {
				running = true
				helper.Coordinator.Run()
			}
			if helper.Vms[0].State() == vm.StateTerminated {
				debugShell.Println("Can not step. Programm already terminated.")
				return
			}
			helper.Vms[helper.CurrentScript].Step()
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "break",
		Aliases: []string{"b"},
		Help:    "add breakpoint at line",
		Func: func(c *ishell.Context) {
			if len(c.Args) != 1 {
				debugShell.Println("You must enter a line number for the breakpoint.")
				return
			}
			line, err := strconv.Atoi(c.Args[0])
			if err != nil {
				debugShell.Println("Error parsing line-number: ", err)
				return
			}

			if validBps, exists := helper.ValidBreakpoints[helper.CurrentScript]; exists {
				if _, isValid := validBps[line]; !isValid {
					debugShell.Println("You can not set a breakpoint at this line")
					return
				}
			}

			helper.Vms[helper.CurrentScript].AddBreakpoint(line)
			debugShell.Println("--Breakpoint added--")
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "delete",
		Aliases: []string{"d"},
		Help:    "delete breakpoint at line",
		Func: func(c *ishell.Context) {
			if len(c.Args) != 1 {
				debugShell.Println("You must enter a line number for the breakpoint.")
				return
			}
			line, err := strconv.Atoi(c.Args[0])
			if err != nil {
				debugShell.Println("Error parsing line-number: ", err)
				return
			}
			helper.Vms[helper.CurrentScript].RemoveBreakpoint(line)
			debugShell.Println("--Breakpoint removed--")
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "vars",
		Aliases: []string{"v"},
		Help:    "print all current variables",
		Func: func(c *ishell.Context) {
			debugShell.Println("--Variables--")
			vars := sortVariables(helper.Vms[helper.CurrentScript].GetVariables())
			// if there is a translation table for this script, translate the internal variable names
			// back to human-readable names
			if helper.VariableTranslations[helper.CurrentScript] != nil {
				for i, v := range vars {
					translated, exists := helper.VariableTranslations[helper.CurrentScript][v.name]
					if exists {
						v.name = fmt.Sprintf("%s (short=%s)", translated, v.name)
						vars[i] = v
					}
				}
			}
			for _, variable := range vars {
				debugShell.Println(variable.name, variable.val.Repr())
			}
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "set",
		Aliases: []string{"w"},
		Help:    "set a variable",
		Func: func(c *ishell.Context) {
			if len(c.Args) != 2 {
				debugShell.Println("You must enter a variable-name and a value")
				return
			}
			varname := helper.ReverseVarnameTranslation(helper.CurrentScript, c.Args[0])
			varvalue := vm.VariableFromString(c.Args[1])
			helper.CurrentVM().SetVariable(varname, varvalue)
			debugShell.Print(varvalue.TypeName())
			debugShell.Println("--Variable set--")
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "info",
		Aliases: []string{"i"},
		Help:    "show vm-state",
		Func: func(c *ishell.Context) {
			c.ShowPrompt(false)
			defer c.ShowPrompt(true)
			statestr := ""
			switch helper.Vms[helper.CurrentScript].State() {
			case vm.StateRunning:
				statestr = "RUNNING"
			case vm.StatePaused:
				statestr = "PAUSED"
			case vm.StateStepping:
				statestr = "STEPPING"
			case vm.StateTerminated:
				statestr = "DONE"
			}
			debugShell.Printf("--State: %s\n", statestr)
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "list",
		Aliases: []string{"l"},
		Help:    "show programm source code",
		Func: func(c *ishell.Context) {
			current := helper.Vms[helper.CurrentScript].CurrentSourceLine()
			bps := helper.Vms[helper.CurrentScript].ListBreakpoints()
			progLines := strings.Split(helper.Scripts[helper.CurrentScript], "\n")
			debugShell.Println("--Programm--")
			pfx := ""
			for i, line := range progLines {
				if i+1 == current {
					pfx = ">"
				} else {
					pfx = " "
				}
				if contains(bps, i+1) {
					pfx += "x"
				} else {
					pfx += " "
				}
				pfx += fmt.Sprintf("%3d ", i+1)
				debugShell.Println(pfx + line)
			}
		},
	})
	debugShell.AddCmd(&ishell.Cmd{
		Name:    "disas",
		Aliases: []string{"d"},
		Help:    "show yolol code for nolol source",
		Func: func(c *ishell.Context) {
			if !strings.HasSuffix(helper.ScriptNames[helper.CurrentScript], ".nolol") {
				debugShell.Print("Disas is only available when debugging nolol code")
			}
			current := helper.Vms[helper.CurrentScript].CurrentAstLine()
			yolol := helper.CompiledCode[helper.CurrentScript]
			progLines := strings.Split(yolol, "\n")
			debugShell.Println("--Programm--")
			pfx := ""
			for i, line := range progLines {
				if i+1 == current {
					pfx = ">"
				} else {
					pfx = " "
				}
				pfx += fmt.Sprintf("%3d ", i+1)
				debugShell.Println(pfx + line)
			}
		},
	})
}

type namedVariable struct {
	name string
	val  vm.Variable
}

func sortVariables(vars map[string]vm.Variable) []namedVariable {
	sorted := make([]namedVariable, 0, len(vars))
	for k, v := range vars {
		sorted = append(sorted, namedVariable{
			k,
			v,
		})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].name < sorted[j].name
	})
	return sorted
}

func contains(arr []int, val int) bool {
	for _, e := range arr {
		if e == val {
			return true
		}
	}
	return false
}
