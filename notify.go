package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	log "github.com/sirupsen/logrus"
	"os/exec"
	"path"
	"sort"
)

func notify(config notifyConfig, unknown map[unknownRepo]struct{}) {
	{
		noModInfo := map[unknownRepo]struct{}{}
		for repo := range unknown {
			lsModInfo, ok := runCmd("git", "-C", path.Join(
				gitMirrorPath, hex.EncodeToString([]byte(fmt.Sprintf("%s/%s", repo.Owner, repo.Name))),
			), "ls-tree", "--name-only", "HEAD", "module.info")

			if ok && len(lsModInfo) < 1 {
				noModInfo[repo] = struct{}{}
			}
		}

		for repo := range noModInfo {
			delete(unknown, repo)
		}
	}

	if len(unknown) > 0 {
		orderedUnknown := make([]unknownRepo, 0, len(unknown))
		for repo := range unknown {
			orderedUnknown = append(orderedUnknown, repo)
		}

		sort.Slice(orderedUnknown, func(i, j int) bool {
			if orderedUnknown[i].Owner == orderedUnknown[j].Owner {
				return orderedUnknown[i].Name < orderedUnknown[j].Name
			} else {
				return orderedUnknown[i].Owner < orderedUnknown[j].Owner
			}
		})

		log.WithFields(log.Fields{
			"repos": orderedUnknown,
		}).Warn("The repository patterns didn't cover some repositories")

		if config.SNail != "" {
			log.WithFields(log.Fields{"email": config.SNail}).Info("Notifying via s-nail")

			cmd := exec.Command("s-nail", "-s", "dockerweb2 discovered new repos", config.SNail)
			var in, out bytes.Buffer

			in.Write([]byte(`dockerweb2 scanned the repositories as configured and discovered ones which aren't covered by any configured repository pattern (per repository owner):

`))

			for _, repo := range orderedUnknown {
				fmt.Fprintf(&in, "* %s%s/%s\n", githubPrefix, repo.Owner, repo.Name)
			}

			in.Write([]byte(`

Please configure additional patterns which cover them by either including ( \Aiw2-mod-(.+)\z ) or ignoring ( \Ano-mod-() ).`))

			cmd.Stdin = &in
			cmd.Stdout = &out
			cmd.Stderr = &out

			if errRn := cmd.Run(); errRn != nil {
				log.WithFields(log.Fields{
					"email": config.SNail, "error": jsonableError{errRn},
				}).Error("Couldn't notify via s-nail")
			}
		}
	} else {
		log.Trace("The repository patterns covered all repositories")
	}
}
