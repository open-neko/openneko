import numpy as np, wave
SR=48000; DUR=22.5; N=int(SR*DUR)
L=np.zeros(N); R=np.zeros(N); VL=np.zeros(N); VR=np.zeros(N)  # dry / reverb send
rng=np.random.default_rng(7)
def hz(m): return 440*2**((m-69)/12)
def add(sig,t0,g=1.0,pan=0.0,send=0.25):
    i=int(t0*SR); n=min(len(sig),N-i)
    if n<=0: return
    s=sig[:n]*g; l=np.cos((pan+1)*np.pi/4); r=np.sin((pan+1)*np.pi/4)
    L[i:i+n]+=s*l; R[i:i+n]+=s*r; VL[i:i+n]+=s*l*send; VR[i:i+n]+=s*r*send
def env(n,a,d,s=0.0,r=None):
    t=np.arange(n)/SR; e=np.minimum(t/a,1.0) if a>0 else np.ones(n)
    return e*(s+(1-s)*np.exp(-t/d))
def lp(x,fc):  # one-pole lowpass, fc may be array
    a=np.exp(-2*np.pi*np.asarray(fc)/SR)*np.ones(len(x)); y=np.zeros_like(x); p=0.0
    for i in range(len(x)): p=(1-a[i])*x[i]+a[i]*p; y[i]=p
    return y
# chords F major vi-IV-I-V : Dm9, Bbmaj7, Fmaj7, C/E
CH=[[50,57,62,65,69,64+12],[46,53,58,62,65,69],[41,53,57,60,64,67],[40,48,55,60,64,67]]
def chord_at(t): return CH[int(t//2)%4]
# --- pad (detuned saws, filtered; opens at the drop) ---
def saw(f,n,det=0.0):
    t=np.arange(n)/SR; ph=(f*(1+det))*t+rng.random(); return 2*(ph%1)-1
seg=int(2*SR)
for k in range(0,11):
    t0=k*2.0; n=int(2.3*SR); ch=chord_at(t0+0.1)
    if t0>=18.5: break
    x=sum(saw(hz(m),n,d) for m in ch[1:] for d in (-0.003,0.003))/10
    e=np.minimum(np.arange(n)/(0.25*SR),1)*np.minimum((n-np.arange(n))/(0.3*SR),1)
    cut=1300 if t0<3 else 2200
    y=lp(x*e,cut)
    add(y,t0,0.42 if t0<3 else 0.17,pan=-0.2,send=0.5); add(y,t0+0.012,0.12,pan=0.5,send=0.5)
# --- bass (sine + a bit of 2nd harmonic), 8ths pulse from the drop ---
for b in range(6,37):   # beats 3.0 .. 18.5
    t0=b*0.5; ch=chord_at(t0+0.01); f=hz(ch[0]-12+12)
    for h,st in ((0,0.0),(1,0.25)):
        n=int(0.24*SR); tt=np.arange(n)/SR
        s=(np.sin(2*np.pi*f*tt)+0.25*np.sin(4*np.pi*f*tt))*env(n,0.004,0.12,0.4)*np.minimum((n-np.arange(n))/(0.02*SR),1)
        add(s,t0+st,0.30 if h==0 else 0.18,send=0.05)
# --- kick / hats ---
def kick():
    n=int(0.35*SR); t=np.arange(n)/SR; f=48+90*np.exp(-t/0.035); ph=2*np.pi*np.cumsum(f)/SR
    return np.sin(ph)*np.exp(-t/0.16)
def hat(d=0.04):
    n=int(0.12*SR); x=rng.standard_normal(n); x=x-lp(x,6000); return x*np.exp(-np.arange(n)/SR/d)
K=kick()
for b in range(6,37):
    t0=b*0.5
    add(K,t0,0.55,send=0.03)
    add(hat(),t0+0.25,0.07,pan=0.3,send=0.15)
    if b>=14 and b%2==1: add(hat(0.02),t0+0.125,0.035,pan=-0.3,send=0.15)
# soft clap on 2 and 4 from scene 3
def clap():
    n=int(0.25*SR); x=rng.standard_normal(n); x=lp(x,2500)-lp(x,900); return x*np.exp(-np.arange(n)/SR/0.06)
for b in range(14,37):
    if b%2==1: add(clap(),b*0.5,0.18,send=0.4)
# --- pluck arpeggio (from 7.0) ---
def pluck(f,dur=0.45,bright=3500):
    n=int(dur*SR); t=np.arange(n)/SR
    x=(np.sin(2*np.pi*f*t)+0.35*np.sin(4*np.pi*f*t)+0.12*np.sin(6*np.pi*f*t))
    return x*env(n,0.003,0.16)
for s in range(28,74):   # 8ths from 7.0 to 18.5
    t0=s*0.25
    if t0>=18.5: break
    ch=chord_at(t0+0.01); notes=sorted(ch[2:]); m=notes[[0,1,2,3,2,1,3,2][s%8]]+12
    add(pluck(hz(m)),t0,0.07,pan=0.35 if s%2 else -0.35,send=0.45)
# --- SFX (in key, same reverb) ---
def bell(f,dur=1.4):
    n=int(dur*SR); t=np.arange(n)/SR
    return (np.sin(2*np.pi*f*t)*np.exp(-t/0.5)+0.4*np.sin(2*np.pi*f*2.76*t)*np.exp(-t/0.12)+0.2*np.sin(2*np.pi*f*5.4*t)*np.exp(-t/0.05))*np.minimum(t/0.002,1)
def whoosh(dur,rev=False):
    n=int(dur*SR); x=rng.standard_normal(n); e=np.linspace(0,1,n)**2
    if rev: e=e[::-1]
    fc=300+4200*(np.linspace(0,1,n)**2 if not rev else np.linspace(1,0,n)**2)
    return lp(x,fc)*e
# hook: alert ping (A5 -> F5) + sub thud
add(bell(hz(81)),0.12,0.16,pan=0.2,send=0.6); add(bell(hz(77)),0.30,0.12,pan=-0.1,send=0.6)
n=int(0.6*SR); tt=np.arange(n)/SR; add(np.sin(2*np.pi*hz(38)*tt)*np.exp(-tt/0.25),0.62,0.35,send=0.1)
for b in range(1,6): add(hat(0.015),b*0.5,0.05,pan=0.15,send=0.3)
# riser into the drop, whooshes on scene changes
add(whoosh(0.55),2.45,0.10,send=0.4)
add(whoosh(0.45),6.35,0.05,send=0.4)
add(whoosh(0.45),14.4,0.08,send=0.4)
add(whoosh(0.5),17.9,0.08,send=0.4)
# evidence ticks: A5 C6 D6 F6
for t0,m in zip([8.0,8.5,9.0,9.5],[81,84,86,89]): add(pluck(hz(m),0.35),t0,0.13,pan=0.25,send=0.5)
# click + approve chime (Fmaj7 arpeggio) + toast blip
n=int(0.03*SR); x=rng.standard_normal(n); add((x-lp(x,3000))*np.exp(-np.arange(n)/SR/0.006),12.4,0.12,send=0.2)
for i,m in enumerate([77,81,84,88]): add(bell(hz(m),1.2),12.62+i*0.06,0.085,pan=-0.3+0.2*i,send=0.6)
add(pluck(hz(84),0.2),12.95,0.06,send=0.4)
# outro: resolve to F add9, long ring
n=int(4.0*SR); t=np.arange(n)/SR
x=sum(np.sin(2*np.pi*hz(m)*t+rng.random()*6)*(0.9 if m<50 else 0.5) for m in [41,53,57,60,64,67,72])
x=x*np.minimum(t/0.01,1)*np.exp(-t/1.4)/5
add(x,18.5,0.5,send=0.6)
add(K,18.5,0.6,send=0.1); add(bell(hz(89),2.0),18.55,0.08,send=0.7); add(bell(hz(84),2.0),18.7,0.06,pan=0.3,send=0.7)
# --- reverb (stereo noise IR) ---
irn=int(1.9*SR); ti=np.arange(irn)/SR
def ir(): h=rng.standard_normal(irn)*np.exp(-ti/0.5); h=lp(h,5000); h[:int(0.02*SR)]=0; return h/np.sqrt(np.sum(h**2))*0.6
from numpy.fft import rfft, irfft
def conv(a,h): m=len(a)+len(h); nf=1<<(m-1).bit_length(); return irfft(rfft(a,nf)*rfft(h,nf),nf)[:len(a)]
L+=conv(VL,ir()); R+=conv(VR,ir())
st=np.stack([L,R],1)
# fade in/out, master glue
fade=np.ones(N); fi=int(0.02*SR); fade[:fi]=np.linspace(0,1,fi); fo=int(1.2*SR); fade[-fo:]=np.linspace(1,0,fo)**1.5
st*=fade[:,None]
st/=np.max(np.abs(st))+1e-9; st=np.tanh(st*1.4)/np.tanh(1.4)*0.79
w=wave.open('music.wav','wb'); w.setnchannels(2); w.setsampwidth(2); w.setframerate(SR)
w.writeframes((st*32767).astype('<i2').tobytes()); w.close(); print('ok', st.shape)
