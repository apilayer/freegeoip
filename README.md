# VGEOIP

This service is based on the original [Apilayer's Freegeoip project](https://github.com/apilayer/freegeoip), with a little modifications.

## VGEOIP

VGEOIP service primary function is to do geolocation based on IP. It could help detect the city, the country, and so on and so forth.

## Technical overview

There are 3 separate, inter-related parts of VGEOIP:

- `apiserver` package (located at `freegreoip/apiserver`)
- `main` package (located at `freegeoip/cmd/freegeoip`)
- `freegeoip` package (located at the root folder)

The `main` package is the point of entry. This is definitely the package that gets compiled. This package however, is just a _gate_ into `apiserver`, so the actual workload is basically not in the `main` package but in `apiserver.Run()`.

> The service is not complicated although it seems like there is a room for improvement. For instance, why do a separation of package is required between `apiserver` and `freegeoip`.

Things that `apiserver` package does:

| Description | File |
|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------|
| - Read configuration from env. var.<br/>- Setup the configuration object.<br/>- Some of interesting envvar:newrelic config, where to log, DB update interval | config.go |
| - Record database events to prometheus.<br/>- Record country code of the clients to prometheus - Record IP versions counter to prometheus.<br/>- Record the number of active client per protocol to prometheus | metrics.go |
| - Essentially running the server (using TLS/not) | main.go |
| - Return data in CSV/JSON/XML format upon request.<br/>- Perform IP lookup.<br/>- Downloading the lookup database.<br/>- Performing rate limiting (if configured). | api.go |

The core component of the `apiserver` package is the `NewConfig` and `NewHandler` functions that both create a `Config` and `apiHandler` struct respectively. `apiHandler` is a struct that consist the following structure:

```go
type apiHandler struct {
  db    *freegeoip.DB
  conf  *Config
  cors  *cors.Cors
  nrapp newrelic.Application
}
```

However, `NewHandler` does not just create `apiHandler` struct, it actually also create the multiplexer from which all requests come in contact with. So, every single web request is handled by this multiplexer.

However, before it can serve any request, `NewHandler` turns out will also attempt to download a database using the `openDB` function of the same package (`apiserver`).  When the system unable to setup the handler (for a host of reasons, one of which being unable to download the database), the system will fatally exit.

`openDB` basically will download a databse if it doesn't have a copy yet in the local filesystem. And, if we have the license and user ID, it will setup a special configuration that will help us later on to download the paid version of the database.

`openDB` eventually is calling `OpenURL` function of the `freegeoip` package (only associated with `db.go`). This package contains routines that help with:

- Downloading the database
- Opening the database, and setting up the reader/data-access object
- Watching the file when it's created/modified and notify it through appropriate channel back up
- Automatically update the database upon a due time/`backoff` period (default: 1 day)
- Performing `Lookup` of an IP

Once `OpenURL` is called, an `autoUpdate` function will kick in automatically in the background using a goroutine (similar to a Ruby's thread but lightweight). It will check if a newer database exist by checking the `X-Database-MD5` and the file's size.

As we might already guess, there are two kinds of database: the paid version and the free version. If we run the service without the paid license, it will never send a request to download the paid version of the database.

## Deployment: Fargate

In the AWS world, there are many kind of deployment services that one can use to deploy app into it:

- [EC2](https://aws.amazon.com/ec2/): the bare metal
- [AWS Elastic Beanstalk](https://aws.amazon.com/elasticbeanstalk/): easy to use code deployment service somewhat trying to replicate Heroku, with 'but'
- [EKS Elastic Container Service for Kubernetes](https://aws.amazon.com/eks/): Kube-style container-orchestrated deployment. Pretty expensive.
- [ECS Elastic Container Service](https://aws.amazon.com/ecs/): AWS own's answer of container-orchestrated deployment. Not expensive.
- [Fargate](https://aws.amazon.com/fargate/): Disregarding the concept of region and zone, this container-based deployment only ask us about what kind of CPU and Memory do we want, and deploy it to either ECS or EKS-manner (EKS not yet supported as of August 2018). Far so easy to deploy at.

Here is the step by step of deployment to AWS Fargate:

- Install Python
- Install awscli

```
$ pip install awscli
```

- [Configure awscli](https://docs.aws.amazon.com/cli/latest/userguide/cli-chap-getting-started.html): require ACCESS KEY ID of your user account for it to be usable.
- Login to ECR:

```
$ $(aws ecr get-login --no-include-email --region ap-northeast-1)
```

- Build the docker image:

```
$ docker build . -t vgeoip:latest
```

To see list of available images:

```
$ docker images
```

- Construct the docker registry address for AWS:

  - [Find out your account ID](https://console.aws.amazon.com/billing/home?#/account) eg: 607558961840
  - [Determine the region ID](https://docs.aws.amazon.com/general/latest/gr/rande.html), eg: ap-northeast-1 for Tokyo

  Your constructed registry address: 607558961840.dkr.ecr. ap-northeast-1.amazonaws.com
  
- Tag the image with ECR repository tag:

```
$ docker tag vgeoip:latest 607558961840.dkr.ecr.ap-northeast-1.amazonaws.com/vgeoip:latest
```

- Push the tagged image to ECR

  - Ensure the repository (eg: `vgeoip`) is already [created beforehand](https://us-west-2.console.aws.amazon.com/ecs/home?region=us-west-2#/repositories). Click create repository on that page if haven't. Ensure Repository URI matches your ID and your region properly.
  - Push:

  ```
  $ docker push 607558961840.dkr.ecr.ap-northeast-1.amazonaws.com/vgeoip:latest
  ```
