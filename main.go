package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/udhos/equalfile"
	"gopkg.in/yaml.v3"
)

type Config struct {
	PrTitle     string   `yaml:"pr_title"`
	FilesDir    string   `yaml:"files_dir"`
	AuthorLogin string   `yaml:"author_login"`
	Repos       []string `yaml:"repos"`
}

type FilesDiff struct {
	NewFiles     []string
	ChangedFiles []string
}

func checkErr(err error) {
	if err != nil {
		debug.PrintStack()
		log.Fatal(err)
	}
}

func readConfig(filename string) (*Config, error) {
	buf, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	c := &Config{}
	err = yaml.Unmarshal(buf, c)
	if err != nil {
		return nil, fmt.Errorf("in file %q: %w", filename, err)
	}

	return c, err
}

func getFilesDiff(dir string, files []string, strip_prefix string) (*FilesDiff, error) {
	result := &FilesDiff{}
	cmp := equalfile.NewMultiple(nil, equalfile.Options{}, sha256.New(), true)

	for _, file := range files {
		file_rel := strings.TrimPrefix(strings.ReplaceAll(file, "\\", "/"), strip_prefix)
		repo_file := dir + "/" + file_rel

		stat, err := os.Stat(file)
		checkErr(err)

		if stat.IsDir() {
			continue
		}

		stat, err = os.Stat(repo_file)
		if err != nil && !os.IsNotExist(err) {
			log.Fatal(err)
		} else if os.IsNotExist(err) {
			result.NewFiles = append(result.NewFiles, file_rel)
		} else {
			equal, err := cmp.CompareFile(repo_file, file)
			if err != nil {
				return nil, err
			}

			if !equal {
				result.ChangedFiles = append(result.ChangedFiles, file_rel)
			}
		}
	}

	return result, nil
}

func getAllFiles(dir string) ([]string, error) {
	var all_files []string

	err := filepath.Walk(dir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if !info.IsDir() {
				all_files = append(all_files, path)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	return all_files, nil
}

func findPrNumber(repo string, title string, author string) (*int, error) {
	type PrAuthor struct {
		IsBot bool   `yaml:"is_bot"`
		Login string `yaml:"login"`
	}

	type PrListItem struct {
		Author PrAuthor `yaml:"author"`
		Number int      `yaml:"number"`
		Title  string   `yaml:"title"`
	}

	cmd := exec.Command(
		"gh", "pr", "list",
		"-R", fmt.Sprintf("ecsact-dev/%s", repo),
		"--json=title,number,author",
	)
	output, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}

	var items []PrListItem
	err = yaml.Unmarshal(output, &items)
	checkErr(err)

	for _, item := range items {
		if item.Author.Login != author {
			continue
		}
		if item.Title != title {
			continue
		}

		return &item.Number, nil
	}

	return nil, nil
}

func updatePr(
	repo_name string,
	branch_name string,
	repo *git.Repository,
	worktree *git.Worktree,
	prTitle string,
	signature *object.Signature,
) {
	err := worktree.AddGlob(".")
	checkErr(err)

	_, err = worktree.Commit(prTitle, &git.CommitOptions{
		Author: signature,
	})
	checkErr(err)

	cmd := exec.Command("git", "push", "origin", "-u", branch_name, "--force")
	cmd.Dir = "clones/" + repo_name

	err = cmd.Run()
	checkErr(err)
}

func createPr(
	repo_name string,
	branch_name string,
	repo *git.Repository,
	worktree *git.Worktree,
	prTitle string,
	signature *object.Signature,
) {
	err := worktree.AddGlob(".")
	checkErr(err)

	_, err = worktree.Commit(prTitle, &git.CommitOptions{
		Author: signature,
	})
	checkErr(err)

	cmd := exec.Command("git", "push", "origin", "-u", branch_name)
	cmd.Dir = "clones/" + repo_name

	err = cmd.Run()
	checkErr(err)

	cmd = exec.Command(
		"gh", "pr", "create",
		"-R", fmt.Sprintf("ecsact-dev/%s", repo_name),
		"-t", prTitle,
		"-b", "Automatically created by https://github.com/ecsact-dev/ecsact_common",
		"-H", branch_name,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	checkErr(err)
}

// gh pr create -R ecsact-dev/ecsact_runtime -t "chore: sync with ecsact_common" -b "Automatically created by https://github.com/ecsact-dev/ecsact_runtime" -H chore/sync-with-ecsact-common -B main

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	c, err := readConfig("config.yml")
	checkErr(err)

	files, err := getAllFiles(c.FilesDir)
	checkErr(err)

	for _, repo_name := range c.Repos {
		repo_clone_dir := fmt.Sprintf("./clones/%s", repo_name)

		var clone_url string
		gh_token := os.Getenv("GH_TOKEN")
		if gh_token != "" {
			clone_url = fmt.Sprintf("https://%s:%s@github.com/ecsact-dev/%s.git", c.AuthorLogin, gh_token, repo_name)
		} else {
			clone_url = fmt.Sprintf("https://github.com/ecsact-dev/%s.git", repo_name)
		}

		repo, err := git.PlainClone(repo_clone_dir, false, &git.CloneOptions{
			URL: clone_url,
		})
		checkErr(err)

		files_diff, err := getFilesDiff(repo_clone_dir, files, c.FilesDir+"/")
		checkErr(err)

		if len(files_diff.ChangedFiles) == 0 && len(files_diff.NewFiles) == 0 {
			fmt.Printf("No changes for %s\n", repo_name)
			continue
		}

		worktree, err := repo.Worktree()
		checkErr(err)

		head, err := repo.Head()
		checkErr(err)

		branch_name := "chore/sync-with-ecsact-common"

		err = worktree.Checkout(&git.CheckoutOptions{
			Hash:   head.Hash(),
			Branch: plumbing.NewBranchReferenceName(branch_name),
			Create: true,
			Force:  true,
			Keep:   false,
		})
		checkErr(err)

		for _, new_file := range files_diff.NewFiles {
			template_file, err := os.Open(c.FilesDir + "/" + new_file)
			checkErr(err)

			repo_file_path := repo_clone_dir + "/" + new_file
			os.MkdirAll(path.Dir(repo_file_path), os.ModePerm)

			repo_file, err := os.Create(repo_file_path)
			checkErr(err)

			_, err = io.Copy(repo_file, template_file)
			checkErr(err)
		}

		for _, changed_file := range files_diff.ChangedFiles {
			template_file, err := os.Open(c.FilesDir + "/" + changed_file)
			checkErr(err)

			repo_file, err := os.Create(repo_clone_dir + "/" + changed_file)
			checkErr(err)

			_, err = io.Copy(repo_file, template_file)
			checkErr(err)
		}

		pr_num, err := findPrNumber(repo_name, c.PrTitle, c.AuthorLogin)
		checkErr(err)

		if pr_num == nil {
			createPr(repo_name, branch_name, repo, worktree, c.PrTitle, &object.Signature{
				Name:  c.AuthorLogin,
				Email: c.AuthorLogin + "@users.noreply.github.com",
				When:  time.Now(),
			})
		} else {
			updatePr(repo_name, branch_name, repo, worktree, c.PrTitle, &object.Signature{
				Name:  c.AuthorLogin,
				Email: c.AuthorLogin + "@users.noreply.github.com",
				When:  time.Now(),
			})
		}
	}
}
