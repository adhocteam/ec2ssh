package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/aws/aws-sdk-go-v2/aws/external"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: %s [options] {instance id|private IPv4 address|name}

Options:
  -v	        be verbose (passes -v to underlying SSH invocation)
  -p	        path to SSH key files
  -l, --list    list running and pending AWS instances
  -c, --command run a command on the remote server
`, filepath.Base(os.Args[0]))
	os.Exit(1)
}

var verboseFlag bool
var remoteCommand string
var listInstances bool
var kp string

var instIdRe = regexp.MustCompile(`i-[0-9a-fA-F]{8,17}$`)

type Instance struct {
	Name string
	Id   string
	Ip   string
}

type Instances []*Instance

func (s Instances) Len() int {
	return len(s)
}

func (s Instances) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s Instances) Less(i, j int) bool {
	switch strings.Compare(s[i].Name, s[j].Name) {
	case -1:
		return true
	case 1:
		return false
	}
	return s[i].Name > s[j].Name
}

func debugf(format string, args ...interface{}) {
	if verboseFlag {
		log.Printf(format, args...)
	}
}

func printError(err error) {
	if awsErr, ok := err.(awserr.Error); ok {
		log.Println("Error:", awsErr.Code(), awsErr.Message())
	} else {
		log.Println("Error:", err.Error())
	}
	os.Exit(1)
}

func reservationsToInstances(reservations []ec2.RunInstancesOutput) []*Instance {
	var instances []*Instance
	for _, reservation := range reservations {
		for _, instance := range reservation.Instances {
			name := "[None]"
			for _, keys := range instance.Tags {
				if *keys.Key == "Name" {
					name = url.QueryEscape(*keys.Value)
				}
			}
			instances = append(instances, &Instance{Name: name, Id: *instance.InstanceId, Ip: *instance.PrivateIpAddress})
		}
	}
	sort.Sort(Instances(instances))
	return instances
}

func printInstanceList(instances []*Instance) {
	if len(instances) == 0 {
		printError(errors.New("Found no instances"))
	} else {
		writer := tabwriter.NewWriter(os.Stdout, 4, 4, 4, ' ', tabwriter.TabIndent)
		fmt.Fprintln(writer, "Name\tInstance ID\tPrivate IP")
		fmt.Fprintln(writer, "----\t-----------\t----------")
		for _, instance := range instances {
			fmt.Fprintf(writer, "%s\t%s\t%s\n", instance.Name, instance.Id, instance.Ip)
		}
		writer.Flush()
	}
}

// Formats a slice of instance pointers into a table and returns it
func fmtInstanceList(instances []*Instance) string {
	var buf bytes.Buffer
	writer := tabwriter.NewWriter(&buf, 4, 4, 4, ' ', tabwriter.TabIndent)
	fmt.Fprintln(writer, "n\tName\tInstance ID\tPrivate IP")
	fmt.Fprintln(writer, "-\t----\t-----------\t----------")
	for i, instance := range instances {
		fmt.Fprintf(writer, "%d\t%s\t%s\t%s\n", i+1, instance.Name, instance.Id, instance.Ip)
	}
	writer.Flush()
	return buf.String()
}

func init() {
	// default key path to home dir, inherit if env var if set
	p := os.Getenv("HOME") + "/.ssh/"
	if s := os.Getenv("AWS_KEY_PATH"); s != "" {
		p = s
	}

	flag.StringVar(&kp, "p", p, "path to directory with SSH keys, default is $HOME/.ssh")
	flag.BoolVar(&verboseFlag, "v", false, "be verbose")

	flag.BoolVar(&listInstances, "l", false, "show list of running instances and exit")
	flag.BoolVar(&listInstances, "list", false, "show list of running instances and exit")

	flag.StringVar(&remoteCommand, "c", "", "A command to run on the remote server")
	flag.StringVar(&remoteCommand, "command", "", "A command to run on the remote server")
}

// Given an instance name and Id, and a reservation list, return the ec2.Instance
// that matches
func findInstance(instance *Instance, reservations []ec2.RunInstancesOutput) (*ec2.Instance, error) {
	for _, reservation := range reservations {
		for _, ec2Instance := range reservation.Instances {
			if *ec2Instance.InstanceId == instance.Id {
				return &ec2Instance, nil
			}
		}
	}
	return nil, fmt.Errorf("Unable to find instance %#v", instance)
}

// Accepts the user's query and a slice of reservations that match the query.
// Shows the user the instance IDs and allows them to choose one on the command
// line, and returns a pointer to the instance that was chosen
func chooseInstance(lookup string, reservations []ec2.RunInstancesOutput) *ec2.Instance {
	var instanceList = reservationsToInstances(reservations)

	fmt.Printf(`Found more than one instance for '%s'.

Available instances:

%s

Which would you like to connect to? [1]
>>> `, lookup, fmtInstanceList(instanceList))
	var which string
	_, err := fmt.Scanln(&which)
	if err == io.EOF {
		// We're currently in the middle of a line; print a newline to clean up
		// the user's terminal
		fmt.Println("")
		os.Exit(0)
	}

	idx := 1
	if len(which) > 0 {
		idx, err = strconv.Atoi(which)
		if err != nil {
			printError(err)
		}
	}

	if idx < 1 || idx > len(instanceList) {
		printError(fmt.Errorf("Invalid index %d", idx))
	}

	instance, err := findInstance(instanceList[idx-1], reservations)
	if err != nil {
		printError(err)
	}

	return instance
}

func main() {
	log.SetFlags(0)
	log.SetPrefix(filepath.Base(os.Args[0]) + ": ")

	flag.Usage = usage
	flag.Parse()

	cfg, err := external.LoadDefaultAWSConfig()
	if err != nil {
		printError(err)
	}

	svc := ec2.New(cfg)

	var instanceStateFilter = ec2.Filter{
		Name: aws.String("instance-state-name"),
		Values: []string{
			"running",
			"pending",
		},
	}

	if flag.NArg() != 1 {
		if listInstances {
			debugf("aws api: describing instances")
			var params *ec2.DescribeInstancesInput

			params = &ec2.DescribeInstancesInput{
				Filters: []ec2.Filter{
					instanceStateFilter,
				},
			}

			req := svc.DescribeInstancesRequest(params)
			resp, err := req.Send()
			if err != nil {
				printError(err)
			}

			printInstanceList(reservationsToInstances(resp.Reservations))
			os.Exit(0)
		} else {
			flag.Usage()
		}
	}

	lookup := flag.Arg(0)
	var params *ec2.DescribeInstancesInput
	if ip := net.ParseIP(lookup); ip != nil {
		params = &ec2.DescribeInstancesInput{
			Filters: []ec2.Filter{
				{
					Name: aws.String("private-ip-address"),
					Values: []string{
						lookup,
					},
				},
				instanceStateFilter,
			},
		}
	} else if instIdRe.MatchString(lookup) {
		debugf("describing instance(s) by ID")
		params = &ec2.DescribeInstancesInput{
			InstanceIds: []string{lookup},
			Filters: []ec2.Filter{
				instanceStateFilter,
			},
		}
	} else {
		debugf("describing instance(s) by name")
		params = &ec2.DescribeInstancesInput{
			Filters: []ec2.Filter{
				{
					Name:   aws.String("tag:Name"),
					Values: []string{lookup},
				},
				instanceStateFilter,
			},
		}
	}

	debugf("aws api: describing instances")
	req := svc.DescribeInstancesRequest(params)
	resp, err := req.Send()
	if err != nil {
		printError(err)
	}

	debugf("aws api: got %d reservation(s)", len(resp.Reservations))

	var instance *ec2.Instance
	if len(resp.Reservations) == 0 {
		printError(fmt.Errorf("Found no instance '%s'", lookup))
	} else if len(resp.Reservations) == 1 {
		instance = &resp.Reservations[0].Instances[0]
	} else if len(resp.Reservations) > 1 {
		instance = chooseInstance(lookup, resp.Reservations)
	}

	binary, lookErr := exec.LookPath("ssh")
	if lookErr != nil {
		printError(lookErr)
	}

	args := []string{"-i", keypath(*instance.KeyName), "-l", "ec2-user", *instance.PrivateIpAddress}
	if verboseFlag {
		args = append(args, "-v")
	}
	if len(remoteCommand) > 1 {
		args = append(args, remoteCommand)
	}

	cmd := exec.Command(binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	debugf("running command %v", cmd.Args)
	if err := cmd.Run(); err != nil {
		printError(err)
	}
}

func keypath(s string) string {
	debugf("key path is: %s", kp)
	return path.Join(kp, s+".pem")
}
