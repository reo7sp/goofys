run-test: s3proxy.jar
	./test/run-tests.sh

s3proxy.jar:
	wget https://oss.sonatype.org/content/repositories/snapshots/org/gaul/s3proxy/1.5.0-SNAPSHOT/s3proxy-1.5.0-20160209.200515-14-jar-with-dependencies.jar -O s3proxy.jar

get-deps: s3proxy.jar
	go get -t ./...
