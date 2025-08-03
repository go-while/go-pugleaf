#!/bin/bash
wget "ftp://ftp.isc.org/pub/usenet/CONFIG/newsgroups" -O newsgroups.isc.$(date +%s)
wget "ftp://ftp.isc.org/pub/usenet/CONFIG/active" -O active.isc.$(date +%s)
