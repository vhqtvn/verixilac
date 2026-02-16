#!/usr/bin/env bash

REMOTE=ubuntu@vh-bot-1
REMOTE_DIR=/home/ubuntu/apps/verixilac/

rsync -rvic ./deploy/ ${REMOTE}:${REMOTE_DIR}

# shellcheck disable=SC2029
ssh ${REMOTE} "cd $REMOTE_DIR && ./up.sh"
