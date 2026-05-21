package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bouwerp/ageni/internal/homedir"
	"github.com/bouwerp/ageni/internal/mcp"
	"github.com/bouwerp/ageni/internal/memory"
	"github.com/bouwerp/ageni/internal/roles"
	"github.com/bouwerp/ageni/internal/secrets"
	"github.com/bouwerp/ageni/internal/skills"
	"github.com/bouwerp/ageni/internal/tools"
)

func loadSkillRegistry() *skills.Registry {
	skillReg, err := skills.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ageni: skills: %v\n", err)
		return nil
	}
	return skillReg
}

func loadRoleRegistry() *roles.Registry {
	rolesUserDir := ""
	if home, err := homedir.Dir(); err == nil {
		rolesUserDir = filepath.Join(home, ".ageni", "roles")
	}
	roleReg, err := roles.Load(rolesUserDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ageni: roles: %v\n", err)
		return roles.Global
	}
	roles.Global = roleReg
	return roleReg
}

func loadMemoryRegistry() *memory.Registry {
	type memResult struct {
		reg *memory.Registry
		err error
	}
	ch := make(chan memResult, 1)
	go func() {
		reg, err := memory.Load()
		ch <- memResult{reg: reg, err: err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "ageni: memories: %v\n", res.err)
			return nil
		}
		return res.reg
	case <-time.After(3 * time.Second):
		fmt.Fprintf(os.Stderr, "ageni: memories: load timed out, skipping\n")
		return nil
	}
}

func registerWorkerBase(r *tools.Registry, todo *tools.TodoWrite, tr *tools.ChangeTracker, skillReg *skills.Registry, memReg *memory.Registry, mcpTools []tools.Tool, secretStore *secrets.Store) {
	r.Register(secrets.NewGuardedReadFile())
	r.Register(tools.WriteFile{Tracker: tr})
	r.Register(tools.EditFile{Tracker: tr})
	r.Register(tools.MultiEdit{Tracker: tr})
	r.Register(tools.TransactionalEdit{Tracker: tr})
	r.Register(tools.ApplyDiff{Tracker: tr})
	r.Register(tools.ListDir{})
	r.Register(tools.Glob{})
	r.Register(secrets.NewGuardedGrep())
	r.Register(tools.SearchSymbols{})
	r.Register(tools.MakeDir{Tracker: tr})
	r.Register(tools.MoveFile{Tracker: tr})
	r.Register(tools.DeleteFile{Tracker: tr})
	r.Register(tools.RunBash{})
	r.Register(tools.WebFetch{})
	r.Register(tools.WebSearch{})
	r.Register(tools.GitStatus{})
	r.Register(tools.GitDiff{})
	r.Register(tools.GitLog{})
	r.Register(tools.ComputeDiff{})
	r.Register(tools.RunTests{})
	r.Register(tools.GitHub{})
	r.Register(tools.PkgInfo{})
	r.Register(todo)
	r.Register(tools.Simulator{})
	if skillReg != nil {
		r.Register(skills.ReadSkill{Registry: skillReg})
	}
	if memReg != nil {
		r.Register(memory.RememberTool{Reg: memReg})
		r.Register(memory.RecallTool{Reg: memReg})
		r.Register(memory.ForgetTool{Reg: memReg})
	}
	for _, t := range mcpTools {
		r.Register(t)
	}
	if secretStore != nil {
		r.Register(secrets.NewListSecretsTool(secretStore))
		r.Register(secrets.NewRunWithSecretTool(secretStore))
		r.Register(secrets.NewHTTPWithAuthTool(secretStore))
		r.SetScrubber(secretStore.Redactor().Scrub)
	}
}

func registerMasterBase(r *tools.Registry, todo *tools.TodoWrite, skillReg *skills.Registry, memReg *memory.Registry, secretStore *secrets.Store) {
	r.Register(todo)
	if skillReg != nil {
		r.Register(skills.ReadSkill{Registry: skillReg})
	}
	if memReg != nil {
		r.Register(memory.RememberTool{Reg: memReg})
		r.Register(memory.RecallTool{Reg: memReg})
		r.Register(memory.ForgetTool{Reg: memReg})
	}
	if secretStore != nil {
		r.SetScrubber(secretStore.Redactor().Scrub)
	}
}

func loadMCPTools(ctx context.Context) (*mcp.Manager, []tools.Tool) {
	mcpMgr, mcpTools, mcpErr := mcp.LoadAndConnect(ctx)
	if mcpErr != nil {
		fmt.Fprintf(os.Stderr, "ageni: mcp setup: %v\n", mcpErr)
	}
	return mcpMgr, mcpTools
}
