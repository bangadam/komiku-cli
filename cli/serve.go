package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/bangadam/komiku-cli/server"
)

const serveCommandUsage = "usage: komiku-cli serve [--addr 0.0.0.0:8080] [--out DIR] [--json]"

func NewServeCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the local library as an offline web reader",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, _ := cmd.Flags().GetString("addr")
			out, _ := cmd.Flags().GetString("out")
			asJSON, _ := cmd.Flags().GetBool("json")
			return runServe(cmd.Context(), addr, out, asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().String("addr", "127.0.0.1:8080", "listen address (use 0.0.0.0:8080 for LAN)")
	cmd.Flags().String("out", "", "library root to serve")
	cmd.Flags().Bool("json", false, "report URLs as JSON before serving")
	return cmd
}

func runServe(parent context.Context, addr, output string, asJSON bool, stdout interface {
	Write([]byte) (int, error)
}, dependencies Dependencies) error {
	overrides := Overrides{}
	if output != "" {
		overrides.OutputRoot = &output
	}
	config, err := loadEffectiveConfig(dependencies, overrides)
	if err != nil {
		return err
	}
	srv, err := server.New(config.OutputRoot)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	url := serveURL(ln.Addr().String())
	if asJSON {
		if err := json.NewEncoder(stdout).Encode(map[string]string{
			"root": config.OutputRoot,
			"addr": ln.Addr().String(),
			"url":  url,
		}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "serving %s\n", config.OutputRoot)
		fmt.Fprintf(stdout, "local:  %s\n", url)
		for _, lan := range lanURLs(ln.Addr().String()) {
			fmt.Fprintf(stdout, "lan:    %s\n", lan)
		}
		fmt.Fprintf(stdout, "ctrl-c to stop\n")
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx, ln)
}

func serveURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://localhost:8080/"
	}
	if host == "" || net.ParseIP(host).IsLoopback() {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%s/", host, port)
}

func lanURLs(addr string) []string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return nil
	}
	var urls []string
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		ipnet, ok := iface.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		urls = append(urls, fmt.Sprintf("http://%s:%s/", ipnet.IP.String(), port))
	}
	return urls
}
