# A tailscale browser proxy client


Just an experiment. While waiting for tailscale to develop their browser extension I was wondering if it was possible to create a small .exe that could run a tailscale browser proxy for firefox or chrome to use without installing tailscale system wide on a windows pc. And it is possible and it does work with my few minutes of testing. As of now i have only tried it with an ephemeral key generated from Tailscale admin pages/keys. 

**Note that this is just a simple experiment i did for fun, no guarantees that this is at all a good idea - and you probably would not be allowed to use it on your work computer even though it probably technically would run.**

Currently you start it with this command in cmd.exe
```
set TS_AUTHKEY=<your-tailscale-key-here>
tsnet-browser-proxy.exe -v
```
And then start your favorite browser with it, e.g. start chrome --proxy-server="http=127.0.0.1:8384;https=127.0.0.1:8384"

Or in firefox you can add it as a browser proxy in the network settings, and type in the address:port in the http field and select to also use it for https

Its quite rudimentary now, but it could be improved if anyone is interested;
- Maybe put som sort of basic auth on it at least
- Some smart/safe way of letting it store a longer living ts key
- Not necessarily a part of the code, but some convenient way of autostarting it minimized/hidden from a shortcut in your browser

I have of course nothing to do with tailscale, I'm just having fun!

 If you want to talk about it, feel free to contact me [on Matrix](https://matrix.to/#/#whatever:vibb.me)
