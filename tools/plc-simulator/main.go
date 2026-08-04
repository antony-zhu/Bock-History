package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

type registerAssignments map[uint16]uint16

func (a *registerAssignments) String() string {
	return ""
}

func (a *registerAssignments) Set(text string) error {
	addressText, valueText, ok := strings.Cut(text, "=")
	if !ok {
		return fmt.Errorf("register must use ADDRESS=VALUE, got %q", text)
	}
	address, err := strconv.ParseUint(strings.TrimSpace(addressText), 0, 16)
	if err != nil {
		return fmt.Errorf("parse register address: %w", err)
	}
	value, err := strconv.ParseUint(strings.TrimSpace(valueText), 0, 16)
	if err != nil {
		return fmt.Errorf("parse register value: %w", err)
	}
	if *a == nil {
		*a = make(registerAssignments)
	}
	(*a)[uint16(address)] = uint16(value)
	return nil
}

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:1502", "TCP listen address; use the PC Ethernet IP for a direct device link")
	unitID := flag.Uint("unit-id", 1, "Modbus unit ID (1-247)")
	var initialRegisters registerAssignments
	flag.Var(&initialRegisters, "register", "initial holding register, e.g. --register 504=0x0000 (repeatable)")
	flag.Parse()

	if *unitID == 0 || *unitID > 247 {
		fmt.Fprintln(os.Stderr, "--unit-id must be between 1 and 247")
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", *listenAddress, err)
		os.Exit(1)
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := NewServer(byte(*unitID), initialRegisters, os.Stdout)
	fmt.Fprintf(os.Stderr, "PLC simulator listening on %s (unit %d)\n", listener.Addr(), *unitID)
	if err := server.Serve(ctx, listener); err != nil {
		fmt.Fprintf(os.Stderr, "serve PLC simulator: %v\n", err)
		os.Exit(1)
	}
}
