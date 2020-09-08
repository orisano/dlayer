# dlayer
dlayer is docker layer analyzer.

## Installation
```bash
go get github.com/orisano/dlayer
```
or
```
docker pull orisano/dlayer
```

## How to use
```bash 
docker save image:tag | dlayer -n 100 | less
```
or
```bash
docker save -o image.tar image:tag
dlayer -f image.tar -n 1000 -d 10 | less
```

![screenshot](https://github.com/orisano/dlayer/raw/images/images/screenshot.png)

## Related Projects
* [orisano/bctx](https://github.com/orisano/bctx) - for build context analysis

## Author
Nao Yonashiro (@orisano)

## License
MIT
