package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/product"
	"github.com/cosmtrek/mindwalk/internal/registry"
)

// reposCmd manages the owner-curated registry of observable repositories.
func reposCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		return reposList(args[1:])
	case "add":
		return reposAdd(args[1:])
	case "show":
		return reposShow(args[1:])
	case "remove":
		return reposRemove(args[1:])
	case "enable":
		return reposSetEnabled(args[1:], true)
	case "disable":
		return reposSetEnabled(args[1:], false)
	case "edit":
		return reposEdit(args[1:])
	case "validate", "refresh":
		return reposRefresh(args[1:])
	case "discover":
		return reposDiscover(args[1:])
	case "discover-status":
		return reposDiscoverStatus(args[1:])
	case "discover-cancel":
		return reposDiscoverCancel(args[1:])
	case "discovered":
		return reposDiscovered(args[1:])
	case "add-discovered":
		return reposAddDiscovered(args[1:])
	case "hide-discovered":
		return reposSetDiscoveryHidden(args[1:], true)
	case "unhide-discovered":
		return reposSetDiscoveryHidden(args[1:], false)
	default:
		return fmt.Errorf("unknown repos subcommand %q (want list|add|show|remove|enable|disable|edit|validate|refresh|discover|discover-status|discover-cancel|discovered|add-discovered|hide-discovered|unhide-discovered)", args[0])
	}
}

func reposFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("repos "+name, flag.ContinueOnError)
	def, err := registry.DefaultPath(product.DirName)
	if err != nil {
		def = ""
	}
	config := fs.String("config", def, "registry file path")
	return fs, config
}

type optionalString struct {
	value string
	set   bool
}

func (o *optionalString) String() string { return o.value }
func (o *optionalString) Set(value string) error {
	o.value = value
	o.set = true
	return nil
}

func reposAdd(args []string) error {
	fs, config := reposFlags("add")
	name := fs.String("name", "", "display name (defaults to the directory name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mindwalk repos add [-name NAME] <dir>")
	}
	ownerLock, err := registry.AcquireOwnerLock(*config)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	repo, err := r.Add(fs.Arg(0), *name)
	if err != nil {
		return err
	}
	if err := r.Save(); err != nil {
		return err
	}
	fmt.Printf("added %s  %s  %s\n", repo.ID, repo.Name, repo.Path)
	return nil
}

func reposList(args []string) error {
	fs, config := reposFlags("list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	repos := r.List()
	if len(repos) == 0 {
		fmt.Println("no repositories registered — add one with: mindwalk repos add <dir>")
		return nil
	}
	for _, repo := range repos {
		state := "enabled"
		if !repo.Enabled {
			state = "disabled"
		}
		st, err := r.StatusOf(repo.ID)
		desc := ""
		switch {
		case err != nil:
			desc = "status unavailable"
		case st.Missing:
			desc = "MISSING"
		case st.InvalidPath:
			desc = "INVALID PATH"
		case st.Git.IsGit:
			desc = fmt.Sprintf("git %s@%s", st.Git.Branch, st.Git.Commit)
			if st.Git.Dirty {
				desc += " dirty"
			}
		default:
			desc = "not a git repository"
		}
		fmt.Printf("%s  %-20s %-8s %s  (%s)\n", repo.ID, repo.Name, state, repo.Path, desc)
	}
	return nil
}

func reposEdit(args []string) error {
	fs, config := reposFlags("edit")
	var name, group, tags, color optionalString
	fs.Var(&name, "name", "display name")
	fs.Var(&group, "group", "group (empty clears)")
	fs.Var(&tags, "tags", "comma-separated tags (empty clears)")
	fs.Var(&color, "color", "display color (empty clears)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mindwalk repos edit [metadata flags] <id>")
	}
	ownerLock, err := registry.AcquireOwnerLock(*config)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	repo, err := r.Get(fs.Arg(0))
	if err != nil {
		return err
	}
	if name.set {
		repo.Name = strings.TrimSpace(name.value)
	}
	if group.set {
		repo.Group = strings.TrimSpace(group.value)
	}
	if color.set {
		repo.Color = strings.TrimSpace(color.value)
	}
	if tags.set {
		repo.Tags = nil
		for _, tag := range strings.Split(tags.value, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				repo.Tags = append(repo.Tags, tag)
			}
		}
	}
	if err := r.Update(repo.ID, repo.Name, repo.Group, repo.Color, repo.Tags); err != nil {
		return err
	}
	if err := r.Save(); err != nil {
		return err
	}
	fmt.Printf("updated %s\n", repo.ID)
	return nil
}

func reposRefresh(args []string) error {
	fs, config := reposFlags("refresh")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: mindwalk repos refresh [id]")
	}
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	if fs.NArg() == 1 {
		status, err := r.StatusOf(fs.Arg(0))
		if err != nil {
			return err
		}
		return writeJSON("", status)
	}
	statuses := make([]registry.Status, 0, len(r.List()))
	for _, repo := range r.List() {
		status, err := r.StatusOf(repo.ID)
		if err != nil {
			return err
		}
		statuses = append(statuses, status)
	}
	return writeJSON("", statuses)
}

func reposShow(args []string) error {
	fs, config := reposFlags("show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mindwalk repos show <id>")
	}
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	st, err := r.StatusOf(fs.Arg(0))
	if err != nil {
		return err
	}
	return writeJSON("", st)
}

func reposRemove(args []string) error {
	fs, config := reposFlags("remove")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mindwalk repos remove <id>")
	}
	ownerLock, err := registry.AcquireOwnerLock(*config)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	if err := r.Remove(fs.Arg(0)); err != nil {
		return err
	}
	if err := r.Save(); err != nil {
		return err
	}
	fmt.Printf("removed %s (repository contents untouched)\n", fs.Arg(0))
	return nil
}

func reposSetEnabled(args []string, enabled bool) error {
	verb := "enable"
	if !enabled {
		verb = "disable"
	}
	fs, config := reposFlags(verb)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mindwalk repos %s <id>", verb)
	}
	ownerLock, err := registry.AcquireOwnerLock(*config)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	if err := r.SetEnabled(fs.Arg(0), enabled); err != nil {
		return err
	}
	if err := r.Save(); err != nil {
		return err
	}
	fmt.Printf("%sd %s\n", verb, fs.Arg(0))
	return nil
}
