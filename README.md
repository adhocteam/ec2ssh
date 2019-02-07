# ec2ssh
Go wrapper around SSH that speaks AWS API

## Installation

```
$ go get github.com/adhocteam/ec2ssh
```

to upgrade your ec2ssh version, do:

```
$ go get -u github.com/adhocteam/ec2ssh
```

## Usage

### Connecting

```
# by AWS instance ID
$ ec2ssh i-0017c8b3

# by the instance Name tag
$ ec2ssh api-server

# by the instance private IP address
$ ec2ssh 1.2.3.4
```

### Other options/flags

Specify an AWS profile other than "default":

```
$ AWS_PROFILE=altprofile ec2ssh
```

Specify an AWS region other than "us-east-1":

```
$ AWS_REGION=us-west-2 ec2ssh
```

See a list of running/pending instance names and ids:

```
$ ec2ssh --list
```

Run a command on a remote server:

```
$ ec2ssh -c 'echo bananas' <remote-server-name>
```

## Notes

- Assumes you have at least one AWS profile configured. See [AWS docs for details](http://docs.aws.amazon.com/cli/latest/userguide/cli-chap-getting-started.html#cli-quick-configuration).
- The tool assumes you keep all SSH keys in `$HOME/.ssh/`, and match the key name assigned to the EC2 instance. Use `-p` or `AWS_KEY_DIR` to specify an alternate path to your private keys.
