# AWS

## Setup

1. Go to the AWS console.
2. Go to *IAM -> Users -> [client your user]*
3. Click *Security credentials*.
4. Scroll to *Access keys*.
5. Hit *Create access key*. AWS will ask you what it’s for. Pick “Command Line Interface (CLI).”
6. AWS gives you: Access Key ID and Secret Access Key

Then:

```
$ brew install aws
$ aws configure
```

Type in:

- Access Key ID
- Secret Access Key
- Default region: us-west-2
- Default output: json

## Things to do

```
aws ec2 describe-instances \
  --filters "Name=instance-state-name,Values=running" \
  --query "Reservations[].Instances[].[InstanceId,PublicIpAddress,Tags[?Key=='Name'].Value|[0]]" \
  --output table
```

# CI

If you get yourself a user in CI, you can do

```
export NAME=$(whoami)-$(date +%s)
python3 ops/ci-vm.py create | tee ci-vm.log
ENVFILE=$(tail -n1 ci-vm.log)
source "$ENVFILE"
export CTR_HOST=ssh://$VM_USER@$VM_IP
time go test -count -v -parallel=1 ./e1e -vexed |& ts '%.s' | tee test.out

## Clean up
python3 ops/ci-vm.py destroy "$ENVFILE"
```
