FROM debian

RUN sed -i 's/deb.debian.org/mirrors.ustc.edu.cn/g' /etc/apt/sources.list
RUN apt-get update && apt-get install -y ca-certificates
RUN mkdir /app
WORKDIR /app
COPY index.html /app
COPY ./u2init /app
COPY ./resources /app/resources
RUN echo -e '#!/bin/sh\ntrue' > /usr/local/bin/adb && chmod +x /usr/local/bin/adb

ENTRYPOINT ["./u2init"]
