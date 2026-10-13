# ssmate

Reach a private machine in AWS, Azure or OCI by name. No jump host, no public
address, no inbound port, and no need to remember which cloud it lives in.

```
$ ssmate web-1
Connecting to web-1 (i-0a1b2c3d4e5f60718) in prod/eu-west-1 via ssm
ssm-user@ip-10-0-1-5:/$
```

Every one of these clouds solves the same problem the same way: nothing listens
on the machine. Either an agent dials outward and waits, or a gateway sits
inside the machine's own network and the cloud checks who you are before
letting you through. ssmate finds the machine, works out whether it can be
reached and why not, and hands the session to the vendor's own program.

## The machine list

Run `ssmate` with no arguments.

```
  █▀▀ █▀▀ █▀▄▀█ ▄▀█ ▀█▀ █▀▀   6 machines  4 reachable
  ▄▄█ ▄▄█ █ ▀ █ █▀█  █  ██▄   2 aws • 2 azure • 2 oci

  ╭──────────────────────────────────────────────────────────────────────────────────────────╮
  │ ›  press / to search                                                            6 of 6   │
  ╰──────────────────────────────────────────────────────────────────────────────────────────╯
  ╭──────────────────────────────────────────────────────────────────────────────────────────╮
  │    MACHINE              CLOUD  REGION          ACCOUNT    VIA          STATUS      USED  │
  │ ──────────────────────────────────────────────────────────────────────────────────────── │
  │  ● web-1                aws    eu-west-1       prod       ssm          online      2h    │
  │  ● build-02             oci    uk-london-1     default    bastion-fwd  online      -     │
  │  ● db-prod              azure  westeurope      contoso    bastion      online      1d    │
  │  ○ report-3             azure  eastus          contoso    bastion      no tunnel   -     │
  │  ○ batch-7              oci    us-ashburn-1    default    bastion-fwd  no gateway  -     │
  │  ○ old-1                aws    us-east-1       prod       -            stopped     -     │
  ╰──────────────────────────────────────────────────────────────────────────────────────────╯
   enter connect   f forward   d doctor   / search   c cloud   i info   ? help   q quit   1/6
```

Everything you want to do to a machine starts by finding it, so the list does
not stop at *which one*. `enter` opens a shell, `f` forwards a port, `d`
diagnoses why something will not connect, `b` gives it a shorter name.

| Key | |
|-----|---|
| `j` `k` `↑` `↓` `g` `G` `ctrl+d` `ctrl+u` | move |
| `/` | search; `tab` changes what it matches — name, tag, or anything |
| `c` | cycle cloud, and back to all of them |
| `r` | only what can be reached right now |
| `s` | cycle sort: recent, name, cloud, status |
| `x` | clear every filter at once |
| `i` | show detail for the selected machine |
| `?` | help |

Colour carries meaning rather than decoration. Green is reachable from where
you are; amber is *the machine is fine, the way in is not* — an agent that
stopped reporting, a network with no bastion, a bastion that cannot carry a
terminal session, or one that is `blocked`; red is not running.

`blocked` is the status that keeps the list honest. An OCI bastion reports
itself active whether or not it can carry a connection, so each one is judged
on refresh: its client allowlist (a list of private ranges admits nobody on
the internet), and the security lists and network security groups between its
subnet and the machine on port 22. A machine none of its bastions can reach
says so, and why, instead of showing as online. Where a rule names something
that cannot be judged from here, such as another security group, the machine
is not called blocked on a guess; `ssmate doctor <machine> --probe` settles it
by opening a session through the bastion and reading the machine's SSH
greeting, without logging in. `NO_COLOR` and a non-terminal both turn it off, and a terminal
that cannot render Unicode gets an ASCII set instead.

## Ways in

Where a machine lives and how you get into it are separate questions, so
ssmate keeps them apart. Every machine carries the ways in that were found for
it, and the `VIA` column shows the one a session would use.

| Way in | `--via` | Reachable when | What moves the bytes |
|---|---|---|---|
| **AWS Session Manager** | `ssm` | the SSM agent is registered and reporting | `session-manager-plugin` |
| **Azure Arc** | `arc` | the machine's Arc agent is connected | `az` + the `ssh` extension |
| **Azure Bastion** | `bastion` | a Bastion serves the network, is Standard or better, **and** has tunnelling on | `az` + the `bastion` extension |
| **OCI Bastion, port forwarding** | `bastion-fwd` | an active Bastion sits in the machine's VCN | `ssh` |
| **OCI Bastion, managed SSH** | `bastion` | as above, and the machine's Bastion plugin is running | `ssh` |

When a machine has more than one way in, the one that exposes least wins: an
agent that connects out (Session Manager, then Arc, which still wants a login
on the machine) before a bastion, which is an endpoint on the internet.

OCI's managed SSH is the exception to that rule, on measurement. It needs no
key of yours: OCI's agent places the session's one-time key on the machine.
But it took three to five minutes to open, against port forwarding's under
one, and Oracle's list of where it works did not predict where it worked. So
port forwarding is the default, and managed SSH is there for someone with no
key on the machine: `ssmate connect build-02 --via bastion`. When port
forwarding fails on a machine that has it, the error says so. `--via` picks one for a single command, and lists only machines
that have it; `SSMATE_ROUTES=bastion,ssm` changes the default order.

### Machines registered from elsewhere

Two services can carry a session to a machine that is not theirs, and with
either of them nothing on the machine listens: an agent connects out, and your
cloud permissions decide who gets in.

- **AWS Session Manager.** A server in OCI, Azure or your own rack runs the
  same agent EC2 does, registered through a [hybrid
  activation](https://docs.aws.amazon.com/systems-manager/latest/userguide/activations.html).
  No login is needed on the machine. AWS charges per session outside EC2.
- **Azure Arc.** A server outside Azure runs Microsoft's [Connected Machine
  agent](https://learn.microsoft.com/azure/azure-arc/servers/ssh-arc-overview),
  and `az ssh arc` carries the session. You sign in with your Microsoft account
  where the machine has the Entra login extension, or as a local account with
  `--user` (and `--key`). Arc does not take Azure's own virtual machines.

ssmate lists such a machine once, under the cloud it actually runs in, with
`ssm` or `arc` as its way in. The registration and the machine's own record
are tied together by the first of these that answers:

1. what the agent recorded about its source machine, where it records it;
2. an `ssmate:source` tag on the registration or the Arc machine, holding the
   machine's ID in its own cloud;
3. the address and the hostname together, when the pair is unique on both
   sides.

An address alone never links two records, because private addresses repeat
across networks. A link made on address and hostname is shown as such in the
list's detail, in `ssmate info` and in `ssmate doctor`, which prints the one
command that makes it certain:

```
aws ssm add-tags-to-resource --resource-type ManagedInstance --resource-id mi-0abc123 \
    --tags Key=ssmate:source,Value=ocid1.instance.oc1... --region eu-west-1
```

Reading the tag on a Session Manager registration needs
`ssm:ListTagsForResource`; without it ssmate still links on the other two. A
registration or Arc machine that cannot be linked keeps its own row.

### Where each way in can fail

On AWS reachability is a property of the *machine* — is the agent alive?
On Azure and OCI it is a property of the *network* — did anyone build a door?
A perfectly healthy OCI machine can be unreachable because no bastion exists
in its VCN, and the fix is in the network, not on the box. ssmate says which.

Azure has a failure no other cloud here has, and it is worth stating plainly:
a bastion can exist, be running, sit in exactly the right network, work
perfectly in a browser, and still not carry a terminal session. The native
client needs a Standard or Premium SKU *and* tunnelling enabled — two
independent settings. That is the `no tunnel` status above, kept separate from
`no gateway`, because building a second bastion will not fix it.

ssmate holds no credentials of its own. It reuses whatever you already signed
in with — `~/.aws`, `az login`, `~/.oci/config` — and where a password is
needed the vendor's program prompts for it directly.

## Commands

```
ssmate                          # the machine list
ssmate web-1                    # shell, by name / identifier / address / tag / bookmark
ssmate web-1 uptime             # run one command instead of a shell
ssmate web-1 -- tail -n 50 /var/log/syslog
ssmate ls                       # list every machine
ssmate clouds                   # each cloud: configured, accounts, regions, machines
ssmate forward db-1 5432:5432   # tunnel a local port
ssmate doctor web-1             # why it will not connect, and the fix
ssmate info web-1               # one machine as JSON
ssmate bookmark add db prod-db-1
```

`ssmate clouds` answers "what can I reach from here" at a glance, from local
configuration and the cache alone:

```
CLOUD  CONFIGURED  ACCOUNTS       REGIONS  MACHINES  REACHABLE
aws    yes         default, prod  4        212       198
azure  yes         contoso        2        64        57
oci    yes         DEFAULT        2        18        15
```

### Narrowing

These work on every command — `ls`, the machine list, `connect`'s name
resolution and shell completion — and mean the same thing in each. Lists are
comma-separated; matching ignores case.

| Flag | |
|---|---|
| `--cloud aws,oci` | only these clouds |
| `--region eu-west-2,us-west-2` | only these regions |
| `--account prod` | only these profiles, subscriptions or tenancies (`--profile` is the same) |
| `--status "no gateway"` | only machines in these states |
| `--tag Environment=production` | only machines with this tag; `--tag Team` requires the key alone. Repeatable |
| `--reachable` | only what can be reached right now |
| `--via ssm` | only machines that have this way in, which a connection then uses |

None of them is ever required. They narrow a long list or separate two machines
that share a name; the point of the tool is reaching a machine without knowing
which cloud it is in.

A refresh narrowed by cloud, region or account re-reads only that part of the
estate and keeps the rest of the cache — it never saves a partial view as the
whole of it.

### Listing

```
ssmate ls --sort recent          # name, cloud, region, status or recent
ssmate ls -o wide                # with addresses and identifiers
ssmate ls -o ids | xargs ...     # one identifier per line; also names, json
```

### Connecting

| Flag | |
|---|---|
| `--user ubuntu` | log in as this user; defaults to the account the machine was built with |
| `--key ~/.ssh/prod` | your private key for the machine, on clouds reached over SSH |
| `--auth key` | how to log in on Azure: `password`, `key` or `aad`. Bastion defaults to `password`; Arc to `aad` unless `--user` names a local account |
| `--reason "INC-123"` | recorded wherever the cloud keeps an audit log |

On Azure, `password` works on every machine without preparation and never
passes through ssmate — `az` reads it from the terminal itself. `key` uses the
key given with `--key`, and `aad` signs you in with your Entra ID, where the
machine allows it. On OCI your own SSH identity logs you in, from `--key` or
your agent; the throwaway key a bastion session opens with only ever reaches
the bastion.

`--print` shows the exact vendor command instead of running it, so you never
have to take ssmate's word for what it does:

```
$ ssmate forward build-02 5432:5432 --print
oci bastion session create-port-forwarding --bastion-id ocid1.bastion... --target-port 5432 ...
ssh -i <key> -N -L 127.0.0.1:5432:10.0.2.15:5432 -p 22 <session-ocid>@host.bastion.uk-london-1.oci.oraclecloud.com
```

Inventory is cached, so the list opens instantly; `--refresh` rebuilds it. A
full sweep of a few hundred machines across three clouds takes well under a
minute, every enabled region searched in parallel and the clouds swept at once
rather than one after another.

## Diagnosis

`doctor` checks your side first, so a missing binary is never reported as a
cloud permissions error, and it never stops at the first failure — three
problems should take one run to find, not three.

```
$ ssmate doctor report-3
ok azure cli (/opt/homebrew/bin/az)
ok bastion extension (1.4.3)
ok azure sign-in (subscription contoso)
ok target exists (report-3 in contoso/eastus)
ok machine running (running)
ok bastion serves this network (vnet-reports-bastion)
X bastion can carry a terminal session (vnet-reports-bastion is Basic with tunnelling off)
    the Basic SKU has no native client. Upgrade it: az network bastion update
    --name vnet-reports-bastion --resource-group rg-reports --sku Standard --enable-tunneling true

! 7 checks, 1 failed.
```

Every remedy is a command you can copy, not a description of one. For a
machine with no way in that works, `doctor` also prints the steps to register
it with Session Manager or Azure Arc, filled in with its identifier, operating
system and processor, so the fix that needs no open door at all is one paste
away. ssmate prints these; it never runs them.

## Scripting

`ls --json` and `info` emit the same schema, so anything reading one can read
the other.

```
$ ssmate info web-1 | jq -r .private_ip
$ ssmate info web-1 | jq -r .via
$ ssmate info web-1 | jq -r '.routes[] | "\(.kind) \(.status)"'
```

Exit codes are part of the contract and will not be renumbered.

| Code | Meaning |
|------|---------|
| 0 | success |
| 1 | general error |
| 2 | cancelled |
| 3 | target not found |
| 4 | credentials expired or absent |
| 5 | permission denied |
| 6 | target not connected |
| 7 | a prerequisite is missing |

ssmate never prompts when stdin is not a terminal; it fails with a code
instead. One caveat worth knowing: a remote command's own exit status comes
back on clouds reached over SSH, but AWS Session Manager does not carry one, so
there ssmate can only report whether the session itself succeeded.

Shell completion knows your machines:

```
$ ssmate connect queue<TAB>
i-0c4d5e6f7a8b9c0d1   queue-ui aws eu-west-1 online
i-0e1f2a3b4c5d6e7f8   queue-ui aws us-west-2 online
```

A name shared by several machines completes to identifiers, because the bare
name is ambiguous and ssmate would only refuse it later. Completion reads the
cache and never refreshes — a shell that hangs on `Tab` is worse than one that
offers nothing.

## Installation

### macOS, with Homebrew

```bash
brew install Vishnu-Rr/tap/ssmate
```

Upgrade with `brew upgrade --cask Vishnu-Rr/tap/ssmate`, remove with
`brew uninstall --cask ssmate`.

### Linux

Pick the newest version and your processor:

```bash
VERSION=$(curl -s https://api.github.com/repos/Vishnu-Rr/ssmate/releases/latest | grep -o '"tag_name": *"v[^"]*' | sed 's/.*"v//')
ARCH=amd64        # or arm64
BASE=https://github.com/Vishnu-Rr/ssmate/releases/download/v$VERSION
```

Debian and Ubuntu:

```bash
curl -LO $BASE/ssmate_${VERSION}_linux_${ARCH}.deb
sudo dpkg -i ssmate_${VERSION}_linux_${ARCH}.deb
```

RHEL, Fedora, Oracle Linux and Amazon Linux:

```bash
curl -LO $BASE/ssmate_${VERSION}_linux_${ARCH}.rpm
sudo rpm -i ssmate_${VERSION}_linux_${ARCH}.rpm
```

Any other distribution:

```bash
curl -L $BASE/ssmate_${VERSION}_linux_${ARCH}.tar.gz | tar -xz ssmate
sudo install ssmate /usr/local/bin/
```

### Windows

In PowerShell, with the version from the
[releases page](https://github.com/Vishnu-Rr/ssmate/releases):

```powershell
$v = "0.1.2"
Invoke-WebRequest "https://github.com/Vishnu-Rr/ssmate/releases/download/v$v/ssmate_${v}_windows_amd64.zip" -OutFile ssmate.zip
Expand-Archive ssmate.zip -DestinationPath "$env:LOCALAPPDATA\ssmate"
```

Then add `%LOCALAPPDATA%\ssmate` to your `PATH`. On an ARM machine use the
`windows_arm64` file instead.

### From source

With Go 1.26 or newer:

```bash
go install github.com/Vishnu-Rr/ssmate/cmd/ssmate@latest
```

### Checking a download

Every release lists a SHA-256 for each file:

```bash
curl -LO $BASE/ssmate_${VERSION}_checksums.txt
shasum -a 256 -c ssmate_${VERSION}_checksums.txt --ignore-missing    # sha256sum on Linux
```

### What each cloud needs

ssmate holds no credentials and moves no bytes itself: it signs in with what
your cloud's own tools already use, and hands each session to the vendor's
own program. Install the ones for the clouds you use; a cloud you have not
signed in to is simply absent, and ssmate never makes a network call to find
out.

| Cloud | Sign in | Install |
|---|---|---|
| AWS | `aws configure` or `aws sso login` | the [Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html) |
| Azure | `az login` | the [Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli), then `az extension add --name bastion`, and for Arc `az extension add --name ssh` |
| OCI | `oci setup config`, which writes `~/.oci/config` | `ssh` |

### First run

```bash
ssmate doctor     # checks every cloud's sign-in and tools, and prints what is missing
ssmate            # the machine list
```

The first run reads every account and region you are signed in to, which
takes a moment; after that the list opens from a cache, and `--refresh`
rebuilds it.

### Shell completion

Completion offers your machines, regions, accounts and tags.

```bash
ssmate completion zsh > $(brew --prefix)/share/zsh/site-functions/_ssmate   # zsh, macOS
ssmate completion zsh > "${fpath[1]}/_ssmate"                               # zsh, Linux
echo 'source <(ssmate completion bash)' >> ~/.bashrc                        # bash
ssmate completion fish > ~/.config/fish/completions/ssmate.fish            # fish
```

Start a new shell afterwards.

## Why not the vendor CLIs?

`aws ssm start-session`, `az network bastion ssh` and `oci bastion session
create` are exactly what ssmate calls underneath, and if you already know the
identifier, the region, the account and — on Azure — the bastion's name and
resource group, they are perfectly good commands. ssmate exists for the part
before that, and for the part after it goes wrong: finding the machine across
accounts, regions and clouds, and naming the actual cause when it will not
connect.

## Why not the other SSM wrappers?

They are good tools, and if you only have AWS they may well be enough. None of
them reaches more than one cloud, and that is the whole design here: one list,
one search, one diagnosis, whichever cloud a machine happens to be in.

## License

Apache-2.0.
