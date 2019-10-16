=========================
Canvus Google Vision Demo
=========================

This simple demo shows how to use MT Canvus `Web API <https://apps.multitaction.com/mt-canvus/manual/server-installation/web-api.html>`_ in the streaming mode and do something useful with it. The code listens for the new images on  a given canvas and annotate every new image with labeles received from the Google Vision API.

======
How-to
======

1. Configure MT Canvus server and client
2. Change these values in the main.go to your own:

.. code-block:: go

    const (
        serverURL      = "http://localhost:8090/api/v1"
        authToken      = "8W5zQ8nrSHe4NdBG"
        canvasId       = "e90af7cf-2164-4ce3-b831-3fbb7d1449ae"
        gvisionKeyFile = "/home/igor/SRC2/gvision_keys.json"
    )

3. Build and run main.go
4. In the Canvus open the specified canvas and drop a new image. 



