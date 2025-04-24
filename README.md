# Minidisc
Zero-config service discovery for Tailscale networks

With minidisc, you can advertise and discover gRPC or REST services on your
Tailnet with zero configuration. There's no need to run a server either &mdash;
minidisc-enabled services form a simple peer-to-peer network, so as long as a
service is up, you can discover it.

For now, primary support is available for Python and Go. Other languages can
rely on the command line tool `md` as a stop gap.

## Status

At the time of writing, Minidisc is in active use at the author's own work and
has been performing nicely, but overall this system has only little mileage. If
you need something battle-hardened, Minidisc isn't for you yet. But if it looks
useful to you, do give it a try and let me know how it goes!

## How to use

### Client

Minidisc maps service names and sets of key-value labels to IP:port pairs. To
find a service, you specify the name and a (sub)set of labels you care about.
Minidisc then returns the address of the first match it finds.

For example, to create a gRPC channel in Python you can do this:

```python
import grpc
import minidisc

endpoint = minidisc.find_service('myservice', {'env': 'prod'})
channel = grpc.insecure_channel(endpoint)
# ... now use the channel to create gRPC stubs.
```

Or if you'd rather have a list of all available services to pick and choose
from, call `minidisc.list_services()`.

In Go, things work similarly:

```go
import (
    "log"
    "github.com/mscheidegger/minidisc/go/pkg/minidisc"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func main() {
    labels := map[string]string{"env": "prod"}
    addr, err := minidisc.FindService("myservice", labels)
    if err != nil {
	log.Fatalf("Minidisc is unavailable: %v", err)
    }
    clientConn, err := grpc.NewClient(
	addr.String(),
	grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    // ... now use the clientConn.
}
```

If you're limiting yourself to Go and gRPC, there's also a fancier way to do the
same, a custom resolver. With this, you can use URLs to find Minidisc services:

```go
import (
    "github.com/mscheidegger/minidisc/go/pkg/mdgrpc"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func main() {
    mdgrpc.RegisterResolver()
    clientConn, err := grpc.NewClient(
	"minidisc://myservices?env=prod",
	grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    // ... now use the clientConn.
}
```

### Server

A server on the Tailnet advertises its services by starting a Minidisc Registry
and then adding entries. Everything else happens automatically in the
background.

For Go:

```go
import (
    "github.com/mscheidegger/minidisc/go/pkg/minidisc"
)

func main() {
    // Initialise the service at "port", then...

    registry, err := minidisc.StartRegistry()
    if err != nil {
	log.Fatalf("Minidisc is unavailable: %v", err)
    }
    labels := map[string]string{"env": "prod"}
    registry.AdvertiseService(port, "myservice", labels)

    // Now you can enter the serving loop.
}

```

After this, the registry will advertise your service to the Tailnet as long as
your process stays alive (and you don't turn off Tailscale). For Python it's
similar:

```python
import minidisc

# Set up your service...

registry = minidisc.start_registry()
registry.advertise_service(port, 'myservice', {'env': 'prod'})

# Now enter the serving loop.
```

### Command line

In addition to the Go and Python libraries, there's also the command line tool
`md`, which offers similar functionality.

To list all services on the Tailnet:
```shell
md list
```

To find a matching service:
```shell
md find myservice env=prod
```

Most importantly, `md` also lets you advertise services of servers that don't
support Minidisc themselves:

```shell
md advertise my-services.yaml
```

You can find an example config
[here](https://github.com/mscheidegger/minidisc/blob/main/example-cfg.yaml).

## Behind the scenes

TODO: Overview of the peer-to-peer discovery.