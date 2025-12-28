#!/bin/bash

VERSION=$1

if [ "$GITHUB_OUTPUT" != "" ]
then
  echo "version=$VERSION" >> $GITHUB_OUTPUT
fi