# dlayer
dlayer is docker layer analyzer.

## Installation
```bash
go get github.com/orisano/dlayer
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

## Author
Nao YONASHIRO (@orisano)

## License
MIT
