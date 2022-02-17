#!/bin/bash



# Generated by: tyk-ci/wf-gen
# Generated on: Wed Feb 16 18:14:52 UTC 2022

# Generation commands:
# ./pr.zsh -p -base port/releng-templates-3.2.3 -branch port/releng-templates-3.2.3 -title Update release engineering templates el7 r3.2.3 -repos tyk
# m4 -E -DxREPO=tyk


echo "Creating user and group..."
GROUPNAME="tyk"
USERNAME="tyk"

getent group "$GROUPNAME" >/dev/null || groupadd -r "$GROUPNAME"
getent passwd "$USERNAME" >/dev/null || useradd -r -g "$GROUPNAME" -M -s /sbin/nologin -c "Tyk service user" "$USERNAME"


# This stopped being a symlink in PR #3569
if [ -L /opt/tyk-gateway/coprocess/python/proto ]; then
    echo "Removing legacy python protobuf symlink"
    rm /opt/tyk-gateway/coprocess/python/proto
fi
