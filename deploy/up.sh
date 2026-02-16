#!/usr/bin/env bash

docker load -i image.tar.gz

docker stop verixilac-bot
docker rm verixilac-bot

docker run -d --name verixilac-bot -v $(pwd)/data:/data/ --env-file=.env psucoder/verixilac:latest bot