# dlayer
dlayer is docker layer analyzer.

## Installation
```bash
go install github.com/orisano/dlayer
# go get github.com/orisano/dlayer
```
or
```
docker pull orisano/dlayer
```

## How to use
```bash
$ dlayer -h
Usage of dlayer:
  -a	show details
  -d int
    	max depth (default 8)
  -f string
    	image.tar path (default "-")
  -i	interactive mode
  -l int
    	screen line width (default 100)
  -n int
    	max files (default 100)
```

```bash
# recommended
docker save image:tag | dlayer -i
```
or
```bash 
docker save image:tag | dlayer -n 100 | less
```
or
```bash
docker save -o image.tar image:tag
dlayer -f image.tar -n 1000 -d 10 | less
```
or
```bash
# using docker
docker save -o image.tar image:tag
docker run -v $PWD:/workdir -it orisano/dlayer -i -f /workdir/image.tar
```

![screenshot](https://github.com/orisano/dlayer/raw/images/images/screenshot.png)

## Related Projects
* [orisano/bctx](https://github.com/orisano/bctx) - for build context analysis

## Author
Nao Yonashiro (@orisano)

## License
MIT
