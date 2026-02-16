#!/usr/bin/env bash

# TAG="v$(date -u +"%Y%m%d")-$(git rev-parse --short HEAD)"
TAG=latest
export TAG

export SERVICE=$1

REPOSITORY=psucoder/verixilac
export REPOSITORY

IMAGE=${REPOSITORY}:${TAG}
export IMAGE

echo "Build ${IMAGE}"

docker build -t "${IMAGE}" -f ./Dockerfile .

# export the image to deploy/image.tar.gz
docker save "${IMAGE}" | gzip > deploy/image.tar.gz

exit 0
