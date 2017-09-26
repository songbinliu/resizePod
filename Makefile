OUTPUT_DIR=./_output

build: clean
	go build -o ${OUTPUT_DIR}/kclient ./

test: clean
	@go test -v -race ./...
	
clean:
	rm -rf ${OUTPUT_DIR}/*
