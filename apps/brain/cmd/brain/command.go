package main

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type commandName string

const (
	commandServe   commandName = "serve"
	commandMigrate commandName = "migrate"
	commandReap    commandName = "reap"
	commandWorker  commandName = "worker"
	commandGraph   commandName = "graph"
)

type direction string

const (
	directionUp   direction = "up"
	directionDown direction = "down"
)

type command struct {
	name      commandName
	direction direction
	team      uuid.UUID
}

var (
	errMigrateUsage = errors.New("usage: brain migrate up|down")
	errGraphUsage   = errors.New("usage: brain graph rebuild [--team <id>]")
)

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
	case commandGraph:
		return parseGraph(args[1:])
	default:
		return command{}, fmt.Errorf(
			"unknown command %q, want serve, migrate, reap, worker or graph", args[0])
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

func parseGraph(args []string) (command, error) {
	if len(args) == 0 || args[0] != "rebuild" {
		return command{}, errGraphUsage
	}
	if len(args) == 1 {
		return command{name: commandGraph}, nil
	}
	if len(args) != 3 || args[1] != "--team" {
		return command{}, errGraphUsage
	}

	team, err := uuid.Parse(args[2])
	if err != nil || team == uuid.Nil {
		return command{}, fmt.Errorf("%w: %q is not a team id", errGraphUsage, args[2])
	}
	return command{name: commandGraph, team: team}, nil
}
