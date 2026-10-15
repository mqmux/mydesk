// MyDesk's original endpoint-aligned bilinear + 52/6 five-point kernel,
// expressed as GPU passes. No third-party renderer source is incorporated.
cbuffer Display : register(b0) {
    uint2 SourceSize;
    uint2 TargetSize;
    uint Mode;        // 0 pixel, 1 endpoint-aligned sharp, 2 smooth
    uint UseRepairs;
    uint Sharp;
    uint Reserved;
    uint2 Origin;
    uint2 Padding;
};
Texture2D<float4> Video : register(t0);
Texture2D<float4> Repair : register(t1);
Texture2D<float> Valid : register(t2);
Texture2D<uint> CoordinatesX : register(t3);
Texture2D<uint> CoordinatesY : register(t4);
Texture2D<float> MotionValid : register(t5);

float4 ReadMotionMask(float4 position : SV_Position) : SV_Target {
    float v=MotionValid.Load(int3(int2(position.xy),0));return float4(v,v,v,1);
}

float4 Vertex(uint id : SV_VertexID) : SV_Position {
    float2 uv = float2((id << 1) & 2, id & 2);
    return float4(uv.x * 2 - 1, 1 - uv.y * 2, 0, 1);
}

// t0=R8 luma, t1=R8G8 chroma. Convert once at SOURCE resolution, then use the
// unchanged RGB scaling/sharp and lossless repair passes. Left/centred chroma
// phase and integer rounding match the CPU compatibility conversion.
float2 Chroma(int2 p) {
    p = clamp(p, int2(0, 0), int2(SourceSize / 2) - 1);
    return Repair.Load(int3(p, 0)).rg * 255;
}
float4 ConvertNV12(float4 position : SV_Position) : SV_Target {
    int2 p = int2(position.xy);
    int y = int(round(Video.Load(int3(p, 0)).r * 255));
    int2 a = int2(p.x / 2, (2*p.y-1) >> 2);
    int2 f = int2((p.x & 1)*2, (2*p.y-1) & 3);
    int2 uv = int2(floor((Chroma(a)*(4-f.x)*(4-f.y) + Chroma(a+int2(1,0))*f.x*(4-f.y) +
                        Chroma(a+int2(0,1))*(4-f.x)*f.y + Chroma(a+int2(1,1))*f.x*f.y) / 16 + 0.5));
    int c = y-16, d = uv.x-128, e = uv.y-128;
    int3 rgb = int3((298*c+409*e+128)>>8, (298*c-100*d-208*e+128)>>8,
                   (298*c+516*d+128)>>8);
    return float4(float3(clamp(rgb, 0, 255))/255, 1);
}

// The coefficient lookup is identical to nv12Normalizer's signed 16.16 table.
// Non-601/range variants are normalized on GPU before the SAME chroma phase,
// clipping and RGB conversion as the CPU-NV12 compatibility path.
int NormalizeYUV(int row, int y, int u, int v) {
    int sum = int(CoordinatesX.Load(int3((row*3)*256+y,0,0))) +
              int(CoordinatesX.Load(int3((row*3+1)*256+u,0,0))) +
              int(CoordinatesX.Load(int3((row*3+2)*256+v,0,0)));
    return clamp(((sum+32768)>>16) + (row==0 ? 16 : 128),0,255);
}
int2 RawUV(int2 p) { return int2(round(Chroma(p))); }
int2 InterpolateRaw(int2 p) {
    int2 a=int2(p.x/2,(2*p.y-1)>>2), f=int2((p.x&1)*2,(2*p.y-1)&3);
    return (RawUV(a)*(4-f.x)*(4-f.y)+RawUV(a+int2(1,0))*f.x*(4-f.y)+
            RawUV(a+int2(0,1))*(4-f.x)*f.y+RawUV(a+int2(1,1))*f.x*f.y+8)>>4;
}
int2 NormalizedUV(int2 p) {
    int2 uv=RawUV(p);
    if(Reserved==0) return uv;
    return int2(NormalizeYUV(1,16,uv.x,uv.y),NormalizeYUV(2,16,uv.x,uv.y));
}
float4 ConvertDirectNV12(float4 position : SV_Position) : SV_Target {
    int2 p=int2(position.xy);
    int y=int(round(Video.Load(int3(p,0)).r*255));
    if(Reserved!=0) { int2 uv=InterpolateRaw(p); y=NormalizeYUV(0,y,uv.x,uv.y); }
    int2 a=int2(p.x/2,(2*p.y-1)>>2), f=int2((p.x&1)*2,(2*p.y-1)&3);
    int2 uv=(NormalizedUV(a)*(4-f.x)*(4-f.y)+NormalizedUV(a+int2(1,0))*f.x*(4-f.y)+
             NormalizedUV(a+int2(0,1))*(4-f.x)*f.y+NormalizedUV(a+int2(1,1))*f.x*f.y+8)>>4;
    int c=y-16,d=uv.x-128,e=uv.y-128;
    int3 rgb=int3((298*c+409*e+128)>>8,(298*c-100*d-208*e+128)>>8,(298*c+516*d+128)>>8);
    return float4(float3(clamp(rgb,0,255))/255,1);
}

// Motion eligibility is cumulative until a NEW repair rebases that tile.
// CPU mask updates for other tiles cannot revive an already invalidated patch.
float InvalidateGPURepairs(float4 position : SV_Position) : SV_Target {
    int2 tile=int2(position.xy);
    if(Valid.Load(int3(tile,0))<0.5 || MotionValid.Load(int3(tile,0))<0.5) return 0;
    int changed=0;
    [loop] for(int y=tile.y*64;y<min(tile.y*64+64,int(SourceSize.y));y+=2) {
        [loop] for(int x=tile.x*64;x<min(tile.x*64+64,int(SourceSize.x));x+=2) {
            int3 delta=abs(int3(round(Video.Load(int3(x,y,0)).rgb*255))-int3(round(Repair.Load(int3(x,y,0)).rgb*255)));
            if(any(delta>24)) { if(++changed>=4) return 0; }
        }
    }
    return 1;
}

float4 Source(int2 p) {
    p = clamp(p, int2(0, 0), int2(SourceSize) - 1);
    if (UseRepairs != 0 && Valid.Load(int3(p / 64, 0)) > 0.5 &&
        (Reserved == 0 || MotionValid.Load(int3(p / 64, 0)) > 0.5))
        return Repair.Load(int3(p, 0));
    return Video.Load(int3(p, 0));
}

float4 Scale(float4 position : SV_Position) : SV_Target {
    uint2 p = uint2(position.xy) - Origin;
    if (Mode == 0)
        return float4(Source(int2(((p * 2 + 1) * SourceSize) / (TargetSize * 2))).rgb, 1);
    if (Mode == 1) {
        // Tiny coordinate tables change only on resize. Preserve exact 8-bit
        // fractions without costly integer division for every display pixel.
        uint2 fixed = uint2(CoordinatesX.Load(int3(p.x, 0, 0)),
                            CoordinatesY.Load(int3(p.y, 0, 0)));
        int2 a = int2(fixed / 256);
        int2 f = int2(fixed % 256);
        int4 c00 = int4(round(Source(a) * 255));
        int4 c10 = int4(round(Source(a + int2(1, 0)) * 255));
        int4 c01 = int4(round(Source(a + int2(0, 1)) * 255));
        int4 c11 = int4(round(Source(a + int2(1, 1)) * 255));
        int4 c = (c00 * (256-f.x) * (256-f.y) + c10 * f.x * (256-f.y) +
                  c01 * (256-f.x) * f.y + c11 * f.x * f.y + 32768) >> 16;
        return float4(float3(c.rgb) / 255, 1);
    }
    float2 q = (float2(p) + 0.5) * float2(SourceSize) / float2(TargetSize) - 0.5;
    int2 a = int2(floor(q));
    float2 f = frac(q);
    float4 c = lerp(lerp(Source(a), Source(a + int2(1, 0)), f.x),
                    lerp(Source(a + int2(0, 1)), Source(a + int2(1, 1)), f.x), f.y);
    return float4(c.rgb, 1);
}

float4 Enhance(float4 position : SV_Position) : SV_Target {
    int2 p = int2(uint2(position.xy) - Origin);
    int3 c = int3(round(Video.Load(int3(p, 0)).rgb * 255));
    if (Sharp != 0 && p.x > 0 && p.y > 0 &&
        p.x < int(TargetSize.x)-1 && p.y < int(TargetSize.y)-1) {
        int3 edge = c * 4 - int3(round(Video.Load(int3(p + int2(-1, 0), 0)).rgb * 255))
                         - int3(round(Video.Load(int3(p + int2(1, 0), 0)).rgb * 255))
                         - int3(round(Video.Load(int3(p + int2(0, -1), 0)).rgb * 255))
                         - int3(round(Video.Load(int3(p + int2(0, 1), 0)).rgb * 255));
        c += int3(abs(edge.x) >= 6 ? edge.x * 52 / 256 : 0,
                  abs(edge.y) >= 6 ? edge.y * 52 / 256 : 0,
                  abs(edge.z) >= 6 ? edge.z * 52 / 256 : 0);
    }
    return float4(float3(clamp(c, 0, 255)) / 255, 1);
}
