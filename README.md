# [Icinga Web 2] [Docker] image builder

Builds and deploys a script which clones the latest version
of Icinga Web 2 and optionally specific modules:

```bash
docker run --rm -d -v dockerweb2-data:/data grandmaster/dockerweb2
```

That script is suitable for building an Icinga Web 2 Docker image.

## Setup

1. Create a Git repository with some commits for the script described above
2. Prepare SSH:

```bash
mkdir dockerweb2-data/ssh
ssh-keygen -q -b 4096 -t rsa -N '' -C bot@example.com -f dockerweb2-data/ssh/id_rsa
ssh-keyscan git.example.com > dockerweb2-data/ssh/known_hosts
```

3. Add `dockerweb2-data/ssh/id_rsa.pub` as a deploy key
   with write access to the Git repository
4. Create `dockerweb2-data/config.yml`:

```yaml
#log:
  # How verbosely to log (trace / debug / info / warn / error)
  #level: info
build:
  # When to build and deploy, crontab format
  every: '0 0 * * *'
github:
  # Icinga Web 2 GitHub repository
  framework: Icinga/icingaweb2
  mods:
    # GitHub account to auto-discover Icinga Web 2 modules of
  - user: Icinga
    repos:
      # Pattern of Icinga Web 2 module repositories (with module name in parens),
      # Golang regex format
    - |-
      \Aicingaweb2-module-(.+)\z
deploy:
  # Git repository to deploy the script to
  remote: 'git@git.example.com:jdoe/icingaweb2-docker.git'
  # Git config
  config:
    core.sshCommand: |-
      ssh -i /data/ssh/id_rsa -o UserKnownHostsFile=/data/ssh/known_hosts
    user.name: JD-OE Bot
    user.email: bot@example.com
  # Script name
  script: get-iw2.sh
  # Commit message
  commit: Update get-iw2.sh
```

The daemon reloads its config automatically.

[Icinga Web 2]: https://github.com/Icinga/icingaweb2
[Docker]: https://www.docker.com
