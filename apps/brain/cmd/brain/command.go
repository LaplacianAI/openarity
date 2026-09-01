package main

import (
	"errors"
	"fmt"
)

type commandName string

const (
	commandServe   commandName = "serve"
	commandMigrate commandName = "migrate"
	commandReap    commandName = "reap"
	commandWorker  commandName = "worker"
)

type direction string

const (
	directionUp   direction = "up"
	directionDown direction = "down"
)

type command struct {
	name      commandName
	direction direction
}

var errMigrateUsage = errors.New("usage: brain migrate up|down")

func parse(args []string) (command, error) {
	if len(args) == 0 {
		return command{name: commandServe}, nil
	}

	switch commandName(args[0]) {
	case commandServe:
		return command{name: commandServe}, nil
	case commandMigrate:
		return parseMigrate(args[1:])
	case commandReap:
		return command{name: commandReap}, nil
	case commandWorker:
		return command{name: commandWorker}, nil
	default:
		return command{}, fmt.Errorf(
			"unknown command %q, want serve, migrate, reap or worker", args[0])
	}
}

func parseMigrate(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, errMigrateUsage
	}

	switch d := direction(args[0]); d {
	case directionUp, directionDown:
		return command{name: commandMigrate, direction: d}, nil
	default:
		return command{}, errMigrateUsage
	}
}
