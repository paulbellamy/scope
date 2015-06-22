#!/bin/bash

set -e

. ./config.sh

echo Copying scope images and scripts to hosts
for HOST in $HOSTS; do
    docker_on $HOST load -i ../scope.tar
    cat ../scope| run_on $HOST sh -c "cat > ./scope"
    run_on $HOST chmod a+x ./scope
done
