I checked out Modal today.

My first python version (3.14) flat out didn't work. Oops.

Their shtick is (easier to use) serverless. You define a function that you
want to run remotely, either right now, or as a web service, or whatever,
in Python, and then it runs remotely.

If I were a Python person, I might find the ergonomics nice, but when I see
"code I want running on the server" and "code running locally" mixed in in one
python file, I know that down the line I'm going to have versioning hell. (This
is true of pyspark as well, though I don't have personal experience.) Things
did feel snappy, which was nice, but when I ran one of their demo Jupyter
notebook things, I could see my image being built in real-time, and it wasn't
pre-built for some reason.

Their onboarding experience is cute. As soon as you follow step 2
and run your first modal function, confetti shows up on the browser,
and you get redirected away to the dashboard. The redirect was a bit
unsettling but the confetti was nice.

I found an example wherein you can run a Jupyter notebook inside Modal.
Jupyter notebook has a terminal feature, so that's the way I popped
a shell. It's a Debian thing, pretty minimal.

I managed to use 50 cents of my five dollar credit mostly on that Jupyter
notebook. (They also have their own more native Notebook implementation (that's
also Jupyter at some sense.)

It's not interesting to me as a hosting platform. As a rent-a-GPU (or CPU) by
the second platform maybe. 

It might also work if I needed to run a model somewhere and not use a full-on
API-level thing like fireworks.
