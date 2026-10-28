package staging

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResolveCollisions removes duplicates only from this target's staged bundle.
// The returned native names also allow verification after a prior collision pass.
func ResolveCollisions(bundle, target string, userRoots []string) (map[string]struct{}, error) {
	userSkills, err := UserSkillNames(userRoots)
	if err != nil {
		return nil, err
	}
	if len(userRoots) == 0 {
		return userSkills, nil
	}
	for _, kind := range []struct{ directory, label string }{{"skills", "skill"}, {"commands", "command"}, {"agents", "agent"}} {
		var user, pack map[string]struct{}
		if kind.directory == "skills" {
			user = userSkills
			pack, err = directoryNames(filepath.Join(bundle, kind.directory))
		} else {
			user, err = markdownStems(componentRoots(userRoots, kind.directory)...)
			if err != nil {
				return nil, err
			}
			pack, err = markdownStems(filepath.Join(bundle, kind.directory))
		}
		if err != nil {
			return nil, err
		}
		for _, name := range intersection(user, pack) {
			root := filepath.Join(bundle, kind.directory)
			if kind.directory == "skills" {
				err = removeNamedDirectory(root, name)
			} else {
				err = removeMarkdownStem(root, name)
			}
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "warning: Using user-installed %s `%s`; omitted pack duplicate from %s staged bundle\n", kind.label, name, target)
		}
	}
	return userSkills, nil
}

// UserSkillNames lists skills in the supplied native user artifact roots.
func UserSkillNames(userRoots []string) (map[string]struct{}, error) {
	return directoryNames(componentRoots(userRoots, "skills")...)
}

func componentRoots(roots []string, component string) []string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, filepath.Join(root, component))
	}
	return paths
}

func directoryNames(roots ...string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				result[strings.ToLower(entry.Name())] = struct{}{}
			}
		}
	}
	return result, nil
}

func markdownStems(roots ...string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				result[strings.ToLower(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))] = struct{}{}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func intersection(left, right map[string]struct{}) []string {
	var values []string
	for value := range left {
		if _, exists := right[value]; exists {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func removeNamedDirectory(root, lowerName string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.EqualFold(entry.Name(), lowerName) {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeMarkdownStem(root, lowerStem string) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") && strings.EqualFold(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), lowerStem) {
			return os.Remove(path)
		}
		return nil
	})
}
