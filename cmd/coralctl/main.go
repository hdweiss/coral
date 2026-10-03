// Command coralctl is a mouse-first Kubernetes TUI.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coralctl/internal/config"
	"github.com/hdweiss/coralctl/internal/k8s"
	"github.com/hdweiss/coralctl/internal/theme"
	"github.com/hdweiss/coralctl/internal/ui"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var (
		kubeconfig string
		opts       ui.Options
		demo       bool
		timeout    time.Duration
		themeSpec  string
	)
	cmd := &cobra.Command{
		Use:           "coralctl",
		Short:         "A mouse-first Kubernetes TUI with tree views",
		SilenceUsage:  true,
		SilenceErrors: false,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			silenceKlog()
			var (
				p   k8s.Provider
				err error
			)
			if demo {
				p = k8s.NewDemoProvider()
			} else if p, err = k8s.NewKubeProvider(kubeconfig); err != nil {
				return err
			}
			if opts.Theme, err = theme.Resolve(themeSpec); err != nil {
				return err
			}
			opts.Version = version
			opts.PinsPath = config.PinsPath(demo)
			opts.FieldsPath = config.FieldsPath()
			app, err := ui.New(k8s.NewStore(p, timeout), opts)
			if err != nil {
				return err
			}
			_, err = tea.NewProgram(app).Run()
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default $KUBECONFIG or ~/.kube/config)")
	f.StringVar(&opts.Context, "context", "", "kubeconfig context to start in")
	f.StringVarP(&opts.Namespace, "namespace", "n", "", `namespace to start in ("all" for all namespaces)`)
	f.BoolVarP(&opts.AllNamespaces, "all-namespaces", "A", false, "start in all namespaces")
	f.DurationVar(&opts.Refresh, "refresh", 10*time.Second, "background refresh interval of the visible list (0 disables)")
	f.DurationVar(&timeout, "timeout", 30*time.Second, "timeout for API requests")
	f.BoolVar(&demo, "demo", false, "use a built-in fake cluster")
	f.StringVar(&themeSpec, "theme", envOr("CORALCTL_THEME", "auto"),
		`colors: "auto" (Omarchy's when installed, else coral), "omarchy", a built-in theme (`+strings.Join(theme.Names(), ", ")+`), or a path to an Omarchy colors.toml or theme directory ($CORALCTL_THEME)`)

	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run:   func(cmd *cobra.Command, args []string) { fmt.Println("coralctl", version) },
	})
	return cmd
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// silenceKlog stops client-go from logging over the TUI.
func silenceKlog() {
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	_ = fs.Set("logtostderr", "false")
	_ = fs.Set("alsologtostderr", "false")
	klog.SetOutput(io.Discard)
}
