#!/bin/bash
#

set -e

cd ..
GOOS=linux GOARCH=arm go build
cd -

./update-rpis.py > online-hosts
ansible-playbook -i online-hosts playbooks/update-u2init.yml
